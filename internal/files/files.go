// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/vaelen/wintermute/internal/store"
)

// Sentinel errors.
var (
	ErrNotFound       = errors.New("files: not found")
	ErrSlugTaken      = errors.New("files: slug taken")
	ErrTokenNotFound  = errors.New("files: token not found")
	ErrTokenUsed      = errors.New("files: token already used")
	ErrTokenExpired   = errors.New("files: token expired")
	ErrTokenWrongKind = errors.New("files: token is for a different operation")
	ErrBlobMissing    = errors.New("files: blob missing on disk")
)

// File is one row of the files table.
type File struct {
	ID          int64
	Slug        string
	Hash        string
	Size        int64
	MIME        string
	OwnerID     int64
	CreatedAt   time.Time
	Description string
}

// TokenKind narrows the token's purpose.
type TokenKind string

const (
	KindUpload   TokenKind = "upload"
	KindDownload TokenKind = "download"
)

// Token is one row of the file_tokens table.
type Token struct {
	Value     string
	Kind      TokenKind
	AccountID int64
	FileID    *int64
	Slug      string
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// Service is the files subsystem.
type Service struct {
	db   *store.DB
	root string
}

// NewService constructs a Service whose blobs live under root. root is
// created if it doesn't exist.
func NewService(db *store.DB, root string) (*Service, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("files: mkdir %s: %w", root, err)
	}
	return &Service{db: db, root: root}, nil
}

// Root returns the configured on-disk root directory.
func (s *Service) Root() string { return s.root }

// PutBlob streams r into a temp file, computes its sha256, sniffs mime
// from the first 512 bytes, and renames the temp into the canonical
// hash path. Returns the hex hash, byte size, and sniffed mime type.
// Idempotent: putting the same content twice yields one blob on disk.
func (s *Service) PutBlob(ctx context.Context, r io.Reader) (hash string, size int64, mime string, err error) {
	tmp, err := os.CreateTemp(s.root, ".tmp-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("files: tmp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath) // no-op if it was renamed
	}()

	h := sha256.New()
	sniff := make([]byte, 0, 512)
	buf := make([]byte, 64*1024)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if len(sniff) < 512 {
				take := 512 - len(sniff)
				if take > n {
					take = n
				}
				sniff = append(sniff, buf[:take]...)
			}
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				_ = tmp.Close()
				return "", 0, "", fmt.Errorf("files: write tmp: %w", werr)
			}
			size += int64(n)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_ = tmp.Close()
			return "", 0, "", fmt.Errorf("files: read: %w", rerr)
		}
	}
	if err := tmp.Close(); err != nil {
		return "", 0, "", err
	}
	hash = hex.EncodeToString(h.Sum(nil))
	mime = http.DetectContentType(sniff)

	finalPath := s.blobPath(hash)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return "", 0, "", fmt.Errorf("files: mkdir blob dir: %w", err)
	}
	// Dedup: if a blob with this hash already exists, drop the temp.
	if _, statErr := os.Stat(finalPath); statErr == nil {
		return hash, size, mime, nil
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", 0, "", fmt.Errorf("files: rename: %w", err)
	}
	return hash, size, mime, nil
}

// GetBlob opens the blob with the given hash.
func (s *Service) GetBlob(_ context.Context, hash string) (io.ReadCloser, error) {
	f, err := os.Open(s.blobPath(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrBlobMissing
	}
	return f, err
}

func (s *Service) blobPath(hash string) string {
	if len(hash) < 4 {
		return filepath.Join(s.root, hash)
	}
	return filepath.Join(s.root, hash[0:2], hash[2:4], hash)
}

// NewFile inserts a row in the files table. The blob with the given
// hash must already exist on disk (typically via PutBlob).
func (s *Service) NewFile(ctx context.Context, slug string, ownerID int64, hash string, size int64, mime, description string) (int64, error) {
	var id int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO files(slug, hash, size, mime, owner_id, created_at, description)
			VALUES (?, ?, ?, ?, ?, strftime('%s','now'), ?)
		`, slug, hash, size, mime, ownerID, description)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		if isUniqueConstraint(err) {
			return 0, ErrSlugTaken
		}
		return 0, fmt.Errorf("files: insert: %w", err)
	}
	return id, nil
}

// GetFile returns a File row by slug.
func (s *Service) GetFile(ctx context.Context, slug string) (File, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT id, slug, hash, size, mime, owner_id, created_at, description
		FROM files WHERE slug = ?
	`, slug)
	return scanFile(row)
}

// GetFileByID returns a File row by id.
func (s *Service) GetFileByID(ctx context.Context, id int64) (File, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT id, slug, hash, size, mime, owner_id, created_at, description
		FROM files WHERE id = ?
	`, id)
	return scanFile(row)
}

// ListByOwner returns every file owned by ownerID, newest first.
func (s *Service) ListByOwner(ctx context.Context, ownerID int64) ([]File, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT id, slug, hash, size, mime, owner_id, created_at, description
		FROM files WHERE owner_id = ? ORDER BY created_at DESC, id DESC
	`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFile removes a file row. The blob is left on disk; the janitor
// removes orphan blobs.
func (s *Service) DeleteFile(ctx context.Context, id int64) error {
	var affected int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
		if err != nil {
			return err
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return fmt.Errorf("files: delete: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// IssueUpload creates a fresh upload token valid for ttl.
func (s *Service) IssueUpload(ctx context.Context, accountID int64, slug string, ttl time.Duration) (Token, error) {
	return s.issueToken(ctx, accountID, KindUpload, nil, slug, ttl)
}

// IssueDownload creates a fresh download token bound to fileID.
func (s *Service) IssueDownload(ctx context.Context, accountID, fileID int64, ttl time.Duration) (Token, error) {
	if _, err := s.GetFileByID(ctx, fileID); err != nil {
		return Token{}, fmt.Errorf("files: issue download: %w", err)
	}
	id := fileID
	return s.issueToken(ctx, accountID, KindDownload, &id, "", ttl)
}

func (s *Service) issueToken(ctx context.Context, accountID int64, kind TokenKind, fileID *int64, slug string, ttl time.Duration) (Token, error) {
	val, err := randomTokenValue()
	if err != nil {
		return Token{}, err
	}
	expires := time.Now().Add(ttl)
	var fid sql.NullInt64
	if fileID != nil {
		fid = sql.NullInt64{Int64: *fileID, Valid: true}
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO file_tokens(value, kind, account_id, file_id, slug, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, val, string(kind), accountID, fid, sql.NullString{String: slug, Valid: slug != ""}, expires.Unix())
		return err
	})
	if err != nil {
		return Token{}, fmt.Errorf("files: issue token: %w", err)
	}
	return Token{Value: val, Kind: kind, AccountID: accountID, FileID: fileID, Slug: slug, ExpiresAt: expires}, nil
}

// RedeemToken looks up, validates, and marks-used the token. expectedKind
// is checked inside the same transaction as the mark-used update, so a
// token presented to the wrong endpoint returns ErrTokenWrongKind without
// being burned and remains redeemable at the correct endpoint.
func (s *Service) RedeemToken(ctx context.Context, value string, expectedKind TokenKind) (Token, error) {
	var (
		got       Token
		expiresAt int64
		fileID    sql.NullInt64
		slug      sql.NullString
		usedAt    sql.NullInt64
		kind      string
	)
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT value, kind, account_id, file_id, slug, expires_at, used_at
			FROM file_tokens WHERE value = ?
		`, value)
		if err := row.Scan(&got.Value, &kind, &got.AccountID, &fileID, &slug, &expiresAt, &usedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTokenNotFound
			}
			return err
		}
		if TokenKind(kind) != expectedKind {
			return ErrTokenWrongKind
		}
		if usedAt.Valid {
			return ErrTokenUsed
		}
		if expiresAt <= time.Now().Unix() {
			return ErrTokenExpired
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE file_tokens SET used_at = strftime('%s','now') WHERE value = ?`,
			value)
		return err
	})
	if err != nil {
		return Token{}, err
	}
	got.Kind = TokenKind(kind)
	if fileID.Valid {
		v := fileID.Int64
		got.FileID = &v
	}
	if slug.Valid {
		got.Slug = slug.String
	}
	got.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return got, nil
}

// Janitor deletes expired tokens and orphan blobs whose mtime is older
// than `grace` (so a freshly-uploaded blob racing a NewFile insert isn't
// reaped before the row lands).
func (s *Service) Janitor(ctx context.Context, grace time.Duration) error {
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM file_tokens WHERE expires_at <= ?`,
			time.Now().Unix())
		return err
	}); err != nil {
		return fmt.Errorf("files: janitor tokens: %w", err)
	}
	// Orphan blobs: stat each blob, delete if no file row references its
	// hash and its mtime is older than grace. Walk the two-level layout.
	cutoff := time.Now().Add(-grace)
	return filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		hash := filepath.Base(path)
		if len(hash) != 64 {
			return nil
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		var n int
		if err := s.db.Read().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM files WHERE hash = ?`, hash).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			_ = os.Remove(path)
		}
		return nil
	})
}

type scanner interface {
	Scan(dest ...any) error
}

func scanFile(s scanner) (File, error) {
	var (
		f       File
		created int64
	)
	err := s.Scan(&f.ID, &f.Slug, &f.Hash, &f.Size, &f.MIME, &f.OwnerID, &created, &f.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	f.CreatedAt = time.Unix(created, 0).UTC()
	return f, nil
}

func randomTokenValue() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isUniqueConstraint(err error) bool {
	return err != nil && (containsAny(err.Error(),
		"UNIQUE constraint", "constraint failed: UNIQUE"))
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

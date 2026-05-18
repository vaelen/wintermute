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
	ErrAreaNotFound   = errors.New("files: area not found")
	ErrAreaInUse      = errors.New("files: area in use")
	ErrAreaTaken      = errors.New("files: area slug taken")
	ErrEmptySlug      = errors.New("files: slug is required")
	ErrEmptyName      = errors.New("files: name is required")
)

// DefaultArea is the slug of the seeded fallback file area. Every fresh
// install ships with this row in file_areas.
const DefaultArea = "dropbox"

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
	Area        string
}

// Area is one row of the file_areas table. ACL bands mirror boards: a
// caller's access level (LevelPlayer=1 / LevelBuilder=2 / LevelAdmin=3
// in the boards package) must be ≥ the corresponding min-level to
// read / write / admin the area.
type Area struct {
	Slug          string
	Name          string
	Description   string
	ReadMinLevel  int
	WriteMinLevel int
	AdminMinLevel int
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
// hash must already exist on disk (typically via PutBlob). An empty
// area defaults to DefaultArea ("dropbox"); callers that pass a
// non-empty area get an ErrAreaNotFound when no such file_areas row
// exists.
func (s *Service) NewFile(ctx context.Context, slug string, ownerID int64, hash string, size int64, mime, description, area string) (int64, error) {
	if area == "" {
		area = DefaultArea
	}
	var id int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_areas WHERE slug = ?`, area).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrAreaNotFound
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO files(slug, hash, size, mime, owner_id, created_at, description, area)
			VALUES (?, ?, ?, ?, ?, strftime('%s','now'), ?, ?)
		`, slug, hash, size, mime, ownerID, description, area)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		if errors.Is(err, ErrAreaNotFound) {
			return 0, err
		}
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
		SELECT id, slug, hash, size, mime, owner_id, created_at, description, area
		FROM files WHERE slug = ?
	`, slug)
	return scanFile(row)
}

// GetFileByID returns a File row by id.
func (s *Service) GetFileByID(ctx context.Context, id int64) (File, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT id, slug, hash, size, mime, owner_id, created_at, description, area
		FROM files WHERE id = ?
	`, id)
	return scanFile(row)
}

// ListFilter narrows ListFiles. Each field is independently optional;
// an empty value means "no filter on this dimension". OwnerID 0 is
// treated as "no owner filter".
type ListFilter struct {
	OwnerID int64
	Area    string
}

// ListByOwner returns every file owned by ownerID, newest first.
func (s *Service) ListByOwner(ctx context.Context, ownerID int64) ([]File, error) {
	return s.ListFiles(ctx, ListFilter{OwnerID: ownerID})
}

// ListFiles returns files matching the filter, newest first. With a
// zero ListFilter, every row is returned.
func (s *Service) ListFiles(ctx context.Context, filter ListFilter) ([]File, error) {
	q := `SELECT id, slug, hash, size, mime, owner_id, created_at, description, area
		FROM files`
	var (
		clauses []string
		args    []any
	)
	if filter.OwnerID > 0 {
		clauses = append(clauses, "owner_id = ?")
		args = append(args, filter.OwnerID)
	}
	if filter.Area != "" {
		clauses = append(clauses, "area = ?")
		args = append(args, filter.Area)
	}
	for i, c := range clauses {
		if i == 0 {
			q += " WHERE "
		} else {
			q += " AND "
		}
		q += c
	}
	q += " ORDER BY created_at DESC, id DESC"
	rows, err := s.db.Read().QueryContext(ctx, q, args...)
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

// JanitorStats reports what a Janitor sweep reaped.
type JanitorStats struct {
	TokensReaped int64
	BlobsReaped  int64
}

// Janitor deletes expired tokens and orphan blobs whose mtime is older
// than `grace` (so a freshly-uploaded blob racing a NewFile insert isn't
// reaped before the row lands). Returns the count of rows / files
// removed.
func (s *Service) Janitor(ctx context.Context, grace time.Duration) (JanitorStats, error) {
	var stats JanitorStats
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM file_tokens WHERE expires_at <= ?`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		stats.TokensReaped, _ = res.RowsAffected()
		return nil
	}); err != nil {
		return stats, fmt.Errorf("files: janitor tokens: %w", err)
	}
	// Orphan blobs: stat each blob, delete if no file row references its
	// hash and its mtime is older than grace. Walk the two-level layout.
	cutoff := time.Now().Add(-grace)
	err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
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
			if err := os.Remove(path); err == nil {
				stats.BlobsReaped++
			}
		}
		return nil
	})
	return stats, err
}

// GetArea returns the named area, or ErrAreaNotFound if absent.
func (s *Service) GetArea(ctx context.Context, slug string) (Area, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT slug, name, description, read_min_level, write_min_level, admin_min_level
		FROM file_areas WHERE slug = ?
	`, slug)
	var a Area
	err := row.Scan(&a.Slug, &a.Name, &a.Description,
		&a.ReadMinLevel, &a.WriteMinLevel, &a.AdminMinLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return Area{}, ErrAreaNotFound
	}
	if err != nil {
		return Area{}, err
	}
	return a, nil
}

// ListAreas returns every area, ordered by slug.
func (s *Service) ListAreas(ctx context.Context) ([]Area, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT slug, name, description, read_min_level, write_min_level, admin_min_level
		FROM file_areas ORDER BY slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Area
	for rows.Next() {
		var a Area
		if err := rows.Scan(&a.Slug, &a.Name, &a.Description,
			&a.ReadMinLevel, &a.WriteMinLevel, &a.AdminMinLevel); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateArea inserts a new file area row. Required: Slug, Name. Zero
// min-levels default to "everyone may read/write, admin-only manage"
// — the same bands used by the seeded dropbox row.
func (s *Service) CreateArea(ctx context.Context, a Area) error {
	if a.Slug == "" {
		return ErrEmptySlug
	}
	if a.Name == "" {
		return ErrEmptyName
	}
	if a.ReadMinLevel == 0 {
		a.ReadMinLevel = 1
	}
	if a.WriteMinLevel == 0 {
		a.WriteMinLevel = 1
	}
	if a.AdminMinLevel == 0 {
		a.AdminMinLevel = 3
	}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO file_areas(slug, name, description,
				read_min_level, write_min_level, admin_min_level)
			VALUES (?, ?, ?, ?, ?, ?)
		`, a.Slug, a.Name, a.Description,
			a.ReadMinLevel, a.WriteMinLevel, a.AdminMinLevel)
		return err
	})
	if err != nil {
		if isUniqueConstraint(err) {
			return ErrAreaTaken
		}
		return fmt.Errorf("files: create area: %w", err)
	}
	return nil
}

// DeleteArea removes a file area. Returns ErrAreaInUse if any files
// reference it, ErrAreaNotFound if no such row exists.
func (s *Service) DeleteArea(ctx context.Context, slug string) error {
	var affected int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM files WHERE area = ?`, slug).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrAreaInUse
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM file_areas WHERE slug = ?`, slug)
		if err != nil {
			return err
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAreaInUse) {
			return err
		}
		return fmt.Errorf("files: delete area: %w", err)
	}
	if affected == 0 {
		return ErrAreaNotFound
	}
	return nil
}

// SetACLs replaces the per-file ACL row for (fileID, accountID). If
// perms is 0, the row is removed entirely.
func (s *Service) SetACLs(ctx context.Context, fileID, accountID int64, perms int) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if perms == 0 {
			_, err := tx.ExecContext(ctx,
				`DELETE FROM file_acls WHERE file_id = ? AND account_id = ?`,
				fileID, accountID)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO file_acls(file_id, account_id, perms)
			VALUES (?, ?, ?)
			ON CONFLICT(file_id, account_id) DO UPDATE SET perms = excluded.perms
		`, fileID, accountID, perms)
		return err
	})
	if err != nil {
		return fmt.Errorf("files: set acls: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanFile(s scanner) (File, error) {
	var (
		f       File
		created int64
	)
	err := s.Scan(&f.ID, &f.Slug, &f.Hash, &f.Size, &f.MIME, &f.OwnerID, &created, &f.Description, &f.Area)
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

// isUniqueConstraint reports whether err looks like a SQLite
// UNIQUE-constraint violation. Matched on text because
// modernc.org/sqlite returns its own concrete error type with no
// public constants — see the "Error matching" convention in CLAUDE.md
// for why substring matching is justified here.
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

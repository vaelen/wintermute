// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/store"
	"golang.org/x/crypto/argon2"
)

// AccessLevel labels the broad permission tier an account has.
type AccessLevel string

// Access levels.
const (
	AccessAdmin   AccessLevel = "admin"
	AccessBuilder AccessLevel = "builder"
	AccessPlayer  AccessLevel = "player"
)

// Account is the in-memory representation of a row in the accounts table.
// Terminal preferences are pointers so that "not set" (nil) is distinct
// from "set to the zero value."
type Account struct {
	ID          int64
	Username    string
	AccessLevel AccessLevel
	CreatedAt   time.Time
	LastLoginAt *time.Time

	TerminalEncoding *string
	TerminalWidth    *int
	TerminalHeight   *int
	TerminalColor    *bool
	TerminalDECLines *bool
}

// TerminalPrefs is the subset of an account's fields that the post-login
// `terminal` command and the connect-time prompt persist.
type TerminalPrefs struct {
	Encoding *string
	Width    *int
	Height   *int
	Color    *bool
	DECLines *bool
}

// Errors returned by auth operations.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrUsernameTaken      = errors.New("auth: username already taken")
	ErrAccountNotFound    = errors.New("auth: account not found")
	ErrInvalidUsername    = errors.New("auth: invalid username")
	ErrPasswordTooShort   = errors.New("auth: password too short")
)

// Argon2id parameters. Tuned per OWASP guidance.
const (
	argon2Memory      = 64 * 1024 // 64 MiB
	argon2Iterations  = 3
	argon2Parallelism = 2
	argon2SaltLen     = 16
	argon2KeyLen      = 32

	minUsernameLen = 2
	maxUsernameLen = 32
	minPasswordLen = 6
)

// Store is the auth-layer view of the accounts table.
type Store struct {
	db          *store.DB
	afterCreate func(ctx context.Context, acc *Account) error
}

// NewStore returns a Store backed by the given database handle.
func NewStore(db *store.DB) *Store {
	return &Store{db: db}
}

// SetAfterCreate registers a callback invoked after a successful Create.
// It runs outside the account-creation transaction. If it returns an
// error, Create surfaces that error to the caller; the account itself is
// already committed.
//
// The intended use is to give other layers (e.g. the world layer's player
// body insertion) a place to hook in without auth needing to import them.
func (s *Store) SetAfterCreate(fn func(ctx context.Context, acc *Account) error) {
	s.afterCreate = fn
}

// Count returns the number of accounts currently in the database. Used by
// callers that want to bootstrap the first account as an admin.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("auth: count: %w", err)
	}
	return n, nil
}

// Create creates a new account, hashing the password with argon2id. Returns
// ErrUsernameTaken if the username already exists, ErrInvalidUsername /
// ErrPasswordTooShort on policy violations.
func (s *Store) Create(ctx context.Context, username, password string, level AccessLevel) (*Account, error) {
	if !validUsername(username) {
		return nil, ErrInvalidUsername
	}
	if len(password) < minPasswordLen {
		return nil, ErrPasswordTooShort
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("auth: hash: %w", err)
	}
	var id int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO accounts(username, password_hash, access_level, created_at)
			 VALUES (?, ?, ?, strftime('%s','now'))`,
			username, hash, string(level),
		)
		if err != nil {
			if isUniqueErr(err) {
				return ErrUsernameTaken
			}
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	acc, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.afterCreate != nil {
		if err := s.afterCreate(ctx, acc); err != nil {
			return nil, fmt.Errorf("auth: after-create hook: %w", err)
		}
	}
	return acc, nil
}

// Login verifies the password and returns the account on success.
// Returns ErrInvalidCredentials for both unknown users and bad passwords,
// to avoid leaking which is which.
func (s *Store) Login(ctx context.Context, username, password string) (*Account, error) {
	var (
		id          int64
		hash        string
		accessLevel string
	)
	row := s.db.Read().QueryRowContext(ctx,
		`SELECT id, password_hash, access_level FROM accounts WHERE username = ?`,
		username,
	)
	if err := row.Scan(&id, &hash, &accessLevel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Run a dummy verify so timing leaks less about whether the
			// user exists. The result is discarded.
			_, _ = verifyPassword(dummyHash, password)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("auth: login: %w", err)
	}
	ok, err := verifyPassword(hash, password)
	if err != nil {
		return nil, fmt.Errorf("auth: verify: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	return s.GetByID(ctx, id)
}

// Touch updates last_login_at for the given account.
func (s *Store) Touch(ctx context.Context, accountID int64) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE accounts SET last_login_at = strftime('%s','now') WHERE id = ?`,
			accountID,
		)
		return err
	})
}

// SaveTerminalPrefs persists the per-account terminal preferences. Any
// pointer that is nil in prefs is set to NULL in the database (i.e. "use
// the auto-detected default on next login").
func (s *Store) SaveTerminalPrefs(ctx context.Context, accountID int64, prefs TerminalPrefs) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE accounts
			   SET terminal_encoding  = ?,
			       terminal_width     = ?,
			       terminal_height    = ?,
			       terminal_color     = ?,
			       terminal_dec_lines = ?
			 WHERE id = ?`,
			nullableString(prefs.Encoding),
			nullableInt(prefs.Width),
			nullableInt(prefs.Height),
			nullableBool(prefs.Color),
			nullableBool(prefs.DECLines),
			accountID,
		)
		return err
	})
}

// GetByID returns the account row with the given id, including terminal
// preferences.
func (s *Store) GetByID(ctx context.Context, id int64) (*Account, error) {
	row := s.db.Read().QueryRowContext(ctx,
		`SELECT id, username, access_level, created_at, last_login_at,
		        terminal_encoding, terminal_width, terminal_height,
		        terminal_color, terminal_dec_lines
		   FROM accounts WHERE id = ?`,
		id,
	)
	return scanAccount(row.Scan)
}

// GetByUsername returns the account row with the given username.
func (s *Store) GetByUsername(ctx context.Context, username string) (*Account, error) {
	row := s.db.Read().QueryRowContext(ctx,
		`SELECT id, username, access_level, created_at, last_login_at,
		        terminal_encoding, terminal_width, terminal_height,
		        terminal_color, terminal_dec_lines
		   FROM accounts WHERE username = ?`,
		username,
	)
	return scanAccount(row.Scan)
}

// ListAccounts returns every account ordered by username, ascending.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id, username, access_level, created_at, last_login_at,
		        terminal_encoding, terminal_width, terminal_height,
		        terminal_color, terminal_dec_lines
		   FROM accounts ORDER BY username ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("auth: list: %w", err)
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		acc, err := scanAccount(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *acc)
	}
	return out, rows.Err()
}

// SetAccessLevel updates the access level for the given account. Returns
// ErrAccountNotFound if no row matches id. Rejects unknown level values
// at the application layer; the underlying column is text so the DB
// would happily accept an invented level otherwise.
func (s *Store) SetAccessLevel(ctx context.Context, id int64, level AccessLevel) error {
	switch level {
	case AccessAdmin, AccessBuilder, AccessPlayer:
	default:
		return fmt.Errorf("auth: SetAccessLevel: unknown level %q", level)
	}
	var affected int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE accounts SET access_level = ? WHERE id = ?`,
			string(level), id,
		)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("auth: SetAccessLevel: %w", err)
	}
	if affected == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// scanFunc is the signature of *sql.Row.Scan / *sql.Rows.Scan.
type scanFunc func(dest ...any) error

func scanAccount(scan scanFunc) (*Account, error) {
	var (
		a           Account
		level       string
		createdAt   int64
		lastLoginNS sql.NullInt64
		enc         sql.NullString
		width       sql.NullInt64
		height      sql.NullInt64
		color       sql.NullInt64
		decLines    sql.NullInt64
	)
	if err := scan(&a.ID, &a.Username, &level, &createdAt, &lastLoginNS,
		&enc, &width, &height, &color, &decLines); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAccountNotFound
		}
		return nil, fmt.Errorf("auth: scan: %w", err)
	}
	a.AccessLevel = AccessLevel(level)
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	if lastLoginNS.Valid {
		t := time.Unix(lastLoginNS.Int64, 0).UTC()
		a.LastLoginAt = &t
	}
	if enc.Valid {
		s := enc.String
		a.TerminalEncoding = &s
	}
	if width.Valid {
		v := int(width.Int64)
		a.TerminalWidth = &v
	}
	if height.Valid {
		v := int(height.Int64)
		a.TerminalHeight = &v
	}
	if color.Valid {
		v := color.Int64 != 0
		a.TerminalColor = &v
	}
	if decLines.Valid {
		v := decLines.Int64 != 0
		a.TerminalDECLines = &v
	}
	return &a, nil
}

// ---------------------------------------------------------------------------
// password hashing

// dummyHash is a valid argon2id encoded hash used by Login to keep
// constant-ish timing when the username is unknown.
var dummyHash = mustDummy()

func mustDummy() string {
	h, err := hashPassword("not-a-real-password")
	if err != nil {
		panic(err)
	}
	return h
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argon2Memory, argon2Iterations, argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// Expected: "", "argon2id", "v=...", "m=...,t=...,p=...", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: malformed hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("auth: malformed hash version: %w", err)
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("auth: malformed hash params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("auth: malformed salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("auth: malformed key: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ---------------------------------------------------------------------------
// helpers

func validUsername(u string) bool {
	if len(u) < minUsernameLen || len(u) > maxUsernameLen {
		return false
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// isUniqueErr returns true if err looks like a SQLite UNIQUE constraint
// violation. modernc.org/sqlite encodes these in the error message.
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableBool(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return 1
	}
	return 0
}

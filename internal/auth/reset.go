// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ResetTokenWords is the number of words drawn from the EFF list for one
// reset token. 4 words × log2(7776) ≈ 51 bits of entropy. Adequate for
// a 48-hour single-use credential; not adequate for a long-term secret.
const ResetTokenWords = 4

// ResetTokenTTL is the lifetime of a freshly issued reset token. The
// admin menu uses this as the ttl argument to IssueReset. 48 hours is
// long enough to relay the token over an offline channel (email, SMS,
// voice) without an in-band self-service flow.
const ResetTokenTTL = 48 * time.Hour

//go:embed wordlist.txt
var wordlistData []byte

var (
	wordlistOnce sync.Once
	wordlist     []string
)

// loadWordlist parses wordlist.txt once. Lines starting with '#' and
// blank lines are skipped so the EFF attribution header doesn't bleed
// into the token vocabulary. Panics if the loaded list is empty or
// changes size unexpectedly — the EFF list is fixed at 7776 entries and
// any deviation is a build/embed bug worth crashing for.
func loadWordlist() []string {
	wordlistOnce.Do(func() {
		sc := bufio.NewScanner(strings.NewReader(string(wordlistData)))
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			wordlist = append(wordlist, line)
		}
		if err := sc.Err(); err != nil {
			panic(fmt.Sprintf("auth: scan wordlist: %v", err))
		}
		if len(wordlist) != 7776 {
			panic(fmt.Sprintf("auth: wordlist size = %d, want 7776", len(wordlist)))
		}
	})
	return wordlist
}

// generateResetToken returns a fresh hyphen-joined four-word token.
// Selection draws unbiased indices from crypto/rand; duplicate words
// across positions are allowed (an attacker who guesses a duplicate
// gains no advantage given the index choice was uniform).
func generateResetToken() (string, error) {
	words := loadWordlist()
	n := uint64(len(words))
	picked := make([]string, ResetTokenWords)
	for i := 0; i < ResetTokenWords; i++ {
		idx, err := randIndex(n)
		if err != nil {
			return "", err
		}
		picked[i] = words[idx]
	}
	return strings.Join(picked, "-"), nil
}

// randIndex returns a uniformly distributed integer in [0, n) drawn from
// crypto/rand. Rejection sampling avoids the modulo bias a naive
// `rand % n` would introduce when 2^64 is not a multiple of n.
func randIndex(n uint64) (uint64, error) {
	if n == 0 {
		return 0, errors.New("auth: randIndex: n=0")
	}
	limit := (^uint64(0) / n) * n
	var buf [8]byte
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint64(buf[:])
		if v < limit {
			return v % n, nil
		}
	}
}

// normalizeResetToken canonicalises user input before hash comparison.
// Whitespace is collapsed, separators (spaces, underscores, hyphens) are
// unified to '-', and the result is lowercased. So "Correct Horse Battery
// Staple", "correct_horse_battery_staple", and "Correct-HORSE-battery-Staple"
// all match the canonical "correct-horse-battery-staple".
func normalizeResetToken(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(in))
	prevSep := true
	for _, r := range in {
		switch {
		case r == ' ' || r == '\t' || r == '_' || r == '-':
			if !prevSep {
				b.WriteByte('-')
				prevSep = true
			}
		default:
			lower := r
			if lower >= 'A' && lower <= 'Z' {
				lower += 'a' - 'A'
			}
			b.WriteRune(lower)
			prevSep = false
		}
	}
	out := b.String()
	out = strings.TrimRight(out, "-")
	return out
}

// IssueReset generates a fresh four-word reset token for accountID,
// stores its argon2id hash with reset_expires_at = now + ttl, and returns
// the plaintext token exactly once. Any prior outstanding reset on the
// same account is overwritten — at most one active reset per account.
//
// Returns ErrAccountNotFound if no row matches.
func (s *Store) IssueReset(ctx context.Context, accountID int64, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errors.New("auth: IssueReset: ttl must be positive")
	}
	plaintext, err := generateResetToken()
	if err != nil {
		return "", fmt.Errorf("auth: IssueReset: generate: %w", err)
	}
	hash, err := hashPassword(plaintext)
	if err != nil {
		return "", fmt.Errorf("auth: IssueReset: hash: %w", err)
	}
	expires := time.Now().Add(ttl).Unix()
	var affected int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE accounts SET reset_hash = ?, reset_expires_at = ? WHERE id = ?`,
			hash, expires, accountID,
		)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return "", fmt.Errorf("auth: IssueReset: %w", err)
	}
	if affected == 0 {
		return "", ErrAccountNotFound
	}
	return plaintext, nil
}

// ChangePassword sets a new password for accountID, clearing any
// outstanding reset_hash / reset_expires_at in the same write. Used by
// the forced-password-change flow after redemption succeeds. Returns
// ErrPasswordTooShort if newPassword fails the minimum-length policy.
func (s *Store) ChangePassword(ctx context.Context, accountID int64, newPassword string) error {
	if len(newPassword) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("auth: ChangePassword: hash: %w", err)
	}
	var affected int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE accounts
			    SET password_hash    = ?,
			        reset_hash       = NULL,
			        reset_expires_at = NULL
			  WHERE id = ?`,
			hash, accountID,
		)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("auth: ChangePassword: %w", err)
	}
	if affected == 0 {
		return ErrAccountNotFound
	}
	return nil
}

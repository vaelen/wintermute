// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/vaelen/wintermute/internal/store"
)

// Options configures a Service. All fields have defaults; zero values
// are acceptable. AutoDenyEnabled gates the in-memory + DB insertion
// triggered by Flag — when false, the structured log line still fires
// but no row is created.
type Options struct {
	AutoDenyEnabled         bool
	AutoDenyTTL             time.Duration
	FailedPasswordThreshold int
	FailedPasswordWindow    time.Duration
	EvictionInterval        time.Duration
	Emitter                 *Emitter
	Logger                  *slog.Logger
	Now                     func() time.Time
}

// Service is the M6.6 login-hardening service. It composes the
// disallowed-username DAO, the username-history DAO, the IP deny list
// (DB + cache), the per-IP failed-password tracker, and an optional
// UDP emitter into a single component with one writer mutex.
type Service struct {
	db      *store.DB
	logger  *slog.Logger
	now     func() time.Time
	options Options

	mu       sync.RWMutex
	cache    *ipCache
	attempts *attemptTracker
	emitter  *Emitter
}

// FlagReason names the trigger that caused a Flag invocation. Recorded
// in the deny-list reason column and the structured log line.
type FlagReason string

// FlagReason values.
const (
	FlagReasonDisallowedUsername FlagReason = "disallowed_username"
	FlagReasonFailedPassword     FlagReason = "failed_password_threshold"
	FlagReasonAdmin              FlagReason = "admin"
)

// New returns a Service backed by db. Construction does not touch the
// database; call Start to hydrate the cache and begin background work.
func New(db *store.DB, opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AutoDenyTTL <= 0 {
		opts.AutoDenyTTL = 5 * time.Minute
	}
	if opts.FailedPasswordThreshold <= 0 {
		opts.FailedPasswordThreshold = 5
	}
	if opts.FailedPasswordWindow <= 0 {
		opts.FailedPasswordWindow = time.Minute
	}
	if opts.EvictionInterval <= 0 {
		opts.EvictionInterval = time.Minute
	}
	return &Service{
		db:       db,
		logger:   opts.Logger,
		now:      opts.Now,
		options:  opts,
		cache:    newIPCache(opts.Now),
		attempts: newAttemptTracker(opts.FailedPasswordThreshold, opts.FailedPasswordWindow, opts.Now),
		emitter:  opts.Emitter,
	}
}

// Start hydrates the in-memory deny cache from the DB, purging expired
// temporary rows along the way. Must be called once before the accept
// loop spawns.
func (s *Service) Start(ctx context.Context) error {
	now := s.now()
	if _, err := s.cleanupExpired(ctx, now); err != nil {
		return err
	}
	rows, err := hydrateIPDenials(ctx, s.db.Read(), now)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		e := denyEntry{
			reason:    r.Reason,
			automatic: r.Automatic,
		}
		if r.ExpiresAt != nil {
			e.kind = denyTemp
			e.expiresAt = r.ExpiresAt.Unix()
		} else {
			e.kind = denyPermanent
		}
		s.cache.set(r.IP, e)
	}
	return nil
}

// Close releases the optional UDP emitter. The Service does not own the
// underlying DB handle.
func (s *Service) Close() error {
	if s.emitter == nil {
		return nil
	}
	return s.emitter.Close()
}

// EvictionInterval returns the configured janitor sweep interval. Used
// by the wire-up code in cmd/wintermute/main.go.
func (s *Service) EvictionInterval() time.Duration {
	return s.options.EvictionInterval
}

// IsIPDenied is the accept-time check installed in the telnet and TLS
// accept loops. Returns true if the address has a current deny entry.
// Permanent entries always return true; temporary entries are lazily
// expired as a side effect of the lookup.
func (s *Service) IsIPDenied(addr netip.Addr) bool {
	return s.cache.isDenied(addr)
}

// CheckUsername is the login-prompt check. Returns ErrUsernameDisallowed
// when name appears in the disallowed_usernames table. As a side effect,
// a disallowed match also flags ip (auto-deny). The login path is
// expected to drop the connection silently on a non-nil return.
func (s *Service) CheckUsername(ctx context.Context, ip netip.Addr, name string) error {
	disallowed, err := isDisallowed(ctx, s.db.Read(), name)
	if err != nil {
		return err
	}
	if disallowed {
		if ip.IsValid() {
			if err := s.Flag(ctx, ip, FlagReasonDisallowedUsername); err != nil {
				s.logger.Warn("security: flag failed",
					"session", ip.String(),
					"reason", FlagReasonDisallowedUsername,
					"err", err)
			}
		}
		return ErrUsernameDisallowed
	}
	return nil
}

// RecordFailedPassword bumps the per-IP attempt counter and flags the
// IP when the threshold is crossed. Returns true when the call itself
// triggered the flag — the session layer is expected to drop the
// connection silently in that case.
func (s *Service) RecordFailedPassword(ctx context.Context, ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	if !s.attempts.record(ip) {
		return false
	}
	if err := s.Flag(ctx, ip, FlagReasonFailedPassword); err != nil {
		s.logger.Warn("security: flag failed",
			"session", ip.String(),
			"reason", FlagReasonFailedPassword,
			"err", err)
	}
	s.attempts.reset(ip)
	return true
}

// CheckAvailable is the auth.UsernamePolicy implementation. It composes
// the disallowed-list check and the history-reservation check.
// forAccount = 0 means "new-account creation"; otherwise the account ID
// being renamed (so self-rotation passes the reservation check).
func (s *Service) CheckAvailable(ctx context.Context, name string, forAccount int64) error {
	disallowed, err := isDisallowed(ctx, s.db.Read(), name)
	if err != nil {
		return err
	}
	if disallowed {
		return ErrUsernameDisallowed
	}
	reserved, err := isReserved(ctx, s.db.Read(), name, forAccount)
	if err != nil {
		return err
	}
	if reserved {
		return ErrUsernameReserved
	}
	return nil
}

// Flag is the atomic "add temp deny + emit UDP" operation shared by
// the disallowed-username and failed-password triggers, plus the
// admin Flag path. When AutoDenyEnabled is false, the row is not
// inserted but the log line and UDP emit still fire.
func (s *Service) Flag(ctx context.Context, ip netip.Addr, reason FlagReason) error {
	ttl := s.options.AutoDenyTTL
	expires := s.now().Add(ttl)
	if !s.options.AutoDenyEnabled {
		s.logger.Info("ip flagged",
			"session", ip.String(),
			"reason", reason,
			"kind", "skipped",
			"ttl", int(ttl.Seconds()))
		s.emit(ip)
		return nil
	}
	if err := s.addTempDeny(ctx, ip, expires, "", string(reason), true); err != nil {
		return err
	}
	s.logger.Info("ip flagged",
		"session", ip.String(),
		"reason", reason,
		"kind", "temp",
		"ttl", int(ttl.Seconds()))
	s.emit(ip)
	return nil
}

func (s *Service) emit(ip netip.Addr) {
	if s.emitter == nil {
		return
	}
	s.emitter.Emit(ip)
}

// AddTempDeny inserts a temporary deny for addr with the given TTL.
// addedBy is the account id of the actor (admin) or nil for automatic
// inserts; reason is free-form operator text.
func (s *Service) AddTempDeny(ctx context.Context, addr netip.Addr, ttl time.Duration, addedBy *int64, reason string) error {
	if ttl <= 0 {
		ttl = s.options.AutoDenyTTL
	}
	expires := s.now().Add(ttl)
	if err := s.addTempDeny(ctx, addr, expires, addedByOrZero(addedBy), reason, false); err != nil {
		return err
	}
	s.logger.Info("ip denied",
		"session", addr.String(),
		"kind", "temp",
		"ttl", int(ttl.Seconds()),
		"reason", reason)
	return nil
}

// AddPermanentDeny inserts a permanent deny for addr.
func (s *Service) AddPermanentDeny(ctx context.Context, addr netip.Addr, addedBy *int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return upsertIPDenial(ctx, tx, addr, nil, addedBy, reason, false)
	}); err != nil {
		return err
	}
	s.cache.set(addr, denyEntry{
		kind:      denyPermanent,
		reason:    reason,
		automatic: false,
	})
	s.logger.Info("ip denied",
		"session", addr.String(),
		"kind", "permanent",
		"reason", reason)
	return nil
}

// addTempDeny is the shared internal path used by both Flag and the
// explicit admin AddTempDeny. addedByID = 0 means "no actor recorded".
func (s *Service) addTempDeny(ctx context.Context, addr netip.Addr, expires time.Time, addedByID, reason string, automatic bool) error {
	exp := expires.Unix()
	var added *int64
	if v, ok := decodeAddedBy(addedByID); ok {
		added = &v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return upsertIPDenial(ctx, tx, addr, &exp, added, reason, automatic)
	}); err != nil {
		return err
	}
	s.cache.set(addr, denyEntry{
		kind:      denyTemp,
		expiresAt: exp,
		reason:    reason,
		automatic: automatic,
	})
	return nil
}

// addedByOrZero adapts a *int64 actor id into the string-serialised form
// used by addTempDeny's internal signature. Keeps the call sites readable
// while avoiding two near-identical helpers.
func addedByOrZero(p *int64) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%d", *p)
}

func decodeAddedBy(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, false
	}
	return v, true
}

// RemoveDeny deletes the deny entry for addr (both DB and cache).
// Returns errIPNotFound when no row exists.
func (s *Service) RemoveDeny(ctx context.Context, addr netip.Addr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check existence first so we can report a clean "not found".
	var one int
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT 1 FROM ip_denials WHERE ip = ?`, addr.String(),
	).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errIPNotFound
		}
		return fmt.Errorf("security: lookup before remove: %w", err)
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return deleteIPDenial(ctx, tx, addr)
	}); err != nil {
		return err
	}
	s.cache.remove(addr)
	s.logger.Info("ip denial removed", "session", addr.String())
	return nil
}

// FlushTempDenials removes every temporary row from the cache and DB.
// Returns the count flushed.
func (s *Service) FlushTempDenials(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		n, err := flushTempIPDenials(ctx, tx)
		deleted = n
		return err
	}); err != nil {
		return 0, err
	}
	_ = s.cache.flushTemp()
	if deleted > 0 {
		s.logger.Info("ip denials temp flushed", "count", deleted)
	}
	return int(deleted), nil
}

// ListDenials returns every current deny entry, sorted by IP. permanent
// and temporary rows are both included; expired temporary rows have
// already been purged by lazy eviction or the janitor.
func (s *Service) ListDenials() []IPDenial {
	items := s.cache.snapshot()
	out := make([]IPDenial, 0, len(items))
	for _, it := range items {
		row := IPDenial{
			IP:        it.addr,
			Reason:    it.entry.reason,
			Automatic: it.entry.automatic,
		}
		if it.entry.kind == denyTemp {
			t := time.Unix(it.entry.expiresAt, 0).UTC()
			row.ExpiresAt = &t
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP.String() < out[j].IP.String() })
	return out
}

// DisallowUsername adds name to the disallowed list. Returns
// ErrDisallowReservedKeyword for the account-create sentinel and
// ErrDisallowExistingAccount if a current account row matches.
func (s *Service) DisallowUsername(ctx context.Context, name, reason string, addedBy *int64) error {
	if isReservedKeyword(name) {
		return ErrDisallowReservedKeyword
	}
	exists, err := accountExists(ctx, s.db.Read(), name)
	if err != nil {
		return err
	}
	if exists {
		return ErrDisallowExistingAccount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return disallowUsername(ctx, tx, name, reason, addedBy)
	}); err != nil {
		return err
	}
	s.logger.Info("username disallowed", "name", name, "reason", reason)
	return nil
}

// AllowUsername removes name from the disallowed list. Removing a name
// that isn't present is a no-op.
func (s *Service) AllowUsername(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return allowUsername(ctx, tx, name)
	}); err != nil {
		return err
	}
	s.logger.Info("username allowed", "name", name)
	return nil
}

// ListDisallowed returns every disallowed username row, ordered by name.
func (s *Service) ListDisallowed(ctx context.Context) ([]DisallowedUsername, error) {
	return listDisallowed(ctx, s.db.Read())
}

// IsDisallowed exposes the cached disallow check for the menu /
// admin layer. Goes through the DB rather than a cache because the
// disallow list is operator-curated and not expected to be hot.
func (s *Service) IsDisallowed(ctx context.Context, name string) (bool, error) {
	return isDisallowed(ctx, s.db.Read(), name)
}

// IsReserved exposes the history-reservation check for the admin menu /
// CLI layer.
func (s *Service) IsReserved(ctx context.Context, name string, forAccount int64) (bool, error) {
	return isReserved(ctx, s.db.Read(), name, forAccount)
}

// RecordRename writes the history row that reserves oldName for
// accountID, refreshing renamed_to / renamed_at / renamed_by if a row
// already exists. Intended to be called inside an existing transaction
// (i.e. the same tx that updates accounts.username) so the rename and
// its reservation row land atomically. Exposed for auth.Store.Rename.
func (s *Service) RecordRename(ctx context.Context, tx *sql.Tx, oldName string, accountID int64, newName string, renamedBy *int64) error {
	return recordHistory(ctx, tx, oldName, accountID, newName, renamedBy)
}

// ListUsernameHistory returns history rows. accountID = 0 returns all.
func (s *Service) ListUsernameHistory(ctx context.Context, accountID int64) ([]UsernameHistoryRow, error) {
	return listHistory(ctx, s.db.Read(), accountID)
}

// ReleaseUsernameHistory drops the row for oldName, freeing it for
// re-use. Removing a non-existent row is a no-op.
func (s *Service) ReleaseUsernameHistory(ctx context.Context, oldName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return releaseHistory(ctx, tx, oldName)
	}); err != nil {
		return err
	}
	s.logger.Info("username history released", "old_username", oldName)
	return nil
}

// EvictExpired purges expired temporary rows from both the cache and
// the DB. Returns the rows removed. Called from the janitor goroutine.
func (s *Service) EvictExpired(ctx context.Context) (int, error) {
	return s.cleanupExpired(ctx, s.now())
}

// cleanupExpired performs one janitor pass at the supplied instant.
// Counts the rows removed; safe to call both at Start (with a stale
// cache) and from the ticker.
func (s *Service) cleanupExpired(ctx context.Context, at time.Time) (int, error) {
	var removed []netip.Addr
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		out, err := purgeExpiredIPDenials(ctx, tx, at)
		removed = out
		return err
	}); err != nil {
		return 0, err
	}
	if len(removed) == 0 {
		// Belt-and-suspenders: also walk the cache for expired entries
		// that may have been added through a different writer (impossible
		// today but cheap insurance).
		stale := s.cache.expiredAddrs(at)
		for _, a := range stale {
			s.cache.remove(a)
		}
		return len(stale), nil
	}
	for _, a := range removed {
		s.cache.remove(a)
	}
	_ = s.attempts.gc()
	return len(removed), nil
}

// SnapshotAttempts is a test helper that returns the number of distinct
// IPs currently tracked by the failed-password counter.
func (s *Service) SnapshotAttempts() int {
	s.attempts.mu.Lock()
	defer s.attempts.mu.Unlock()
	return len(s.attempts.hits)
}

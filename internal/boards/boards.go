// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package boards

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	"github.com/vaelen/wintermute/internal/store"
)

// Sentinel errors.
var (
	ErrNotFound       = errors.New("boards: not found")
	ErrUnknownNetwork = errors.New("boards: unknown network")
	ErrForbidden      = errors.New("boards: forbidden")
)

// Min-AccessLevel bands stored in *_perms columns.
//
//	0 = anyone (open)
//	1 = AccessPlayer or higher (any logged-in account)
//	2 = AccessBuilder or higher
//	3 = AccessAdmin only
const (
	LevelOpen    = 0
	LevelPlayer  = 1
	LevelBuilder = 2
	LevelAdmin   = 3
)

// Board mirrors one row of the boards table.
type Board struct {
	ID, NetworkID int64
	Slug, Name    string
	Description   string
	AreaTag       sql.NullString
	ReadMinLevel  int
	PostMinLevel  int
	AdminMinLevel int
}

// Post mirrors one row of the board_posts table.
type Post struct {
	ID, BoardID, NetworkID int64
	AuthorID               sql.NullInt64
	AuthorName             string
	MSGID                  string
	ReplyToMSGID           sql.NullString
	ThreadRootMSGID        string
	OriginAddr             string
	AreaTag                sql.NullString
	Attributes             uint16
	Charset                string
	PID                    sql.NullString
	TZOffset               sql.NullInt64
	Tearline               sql.NullString
	OriginLine             sql.NullString
	SeenBy                 sql.NullString
	Path                   sql.NullString
	Kludges                sql.NullString
	Subject                string
	Body                   string
	PostedAt               time.Time
}

// ThreadSummary is the per-thread row returned by ListThreads.
type ThreadSummary struct {
	RootMSGID  string
	RootPostID int64
	Subject    string
	AuthorName string
	Latest     time.Time
	ReplyCount int
	Unread     bool
}

// ServiceOptions are constructor knobs for NewService.
type ServiceOptions struct {
	// PID is the FSC-0046 PID kludge stamped on every locally-originated
	// post (e.g. "Wintermute/1.2.3").
	PID string
	// ServerName is the human-readable name of this server used in the
	// FTS-0004 origin line, e.g. "Wintermute @ Sprawl".
	ServerName string
	// Tearline is the FTS-0004 tear line stamped on every
	// locally-originated post, e.g. "--- Wintermute/1.2.3".
	Tearline string
}

// Service is the boards subsystem.
type Service struct {
	db     *store.DB
	issuer *msgid.Issuer
	opts   ServiceOptions
}

// NewService constructs a Service.
func NewService(db *store.DB, issuer *msgid.Issuer, opts ServiceOptions) *Service {
	return &Service{db: db, issuer: issuer, opts: opts}
}

// CreateBoardSpec is the input to CreateBoard.
type CreateBoardSpec struct {
	Slug          string
	Name          string
	Description   string
	NetworkSlug   string
	AreaTag       string
	ReadMinLevel  int
	PostMinLevel  int
	AdminMinLevel int
}

// CreateBoard creates a new board in the given network.
func (s *Service) CreateBoard(ctx context.Context, spec CreateBoardSpec) (int64, error) {
	if spec.Slug == "" || spec.Name == "" {
		return 0, errors.New("boards: slug and name required")
	}
	if spec.NetworkSlug == "" {
		spec.NetworkSlug = "local"
	}
	net, err := ftnnetworks.Get(ctx, s.db, spec.NetworkSlug)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrUnknownNetwork, spec.NetworkSlug)
	}
	if spec.AdminMinLevel == 0 {
		spec.AdminMinLevel = LevelAdmin
	}

	var areaTag sql.NullString
	if spec.AreaTag != "" {
		areaTag = sql.NullString{String: spec.AreaTag, Valid: true}
	}

	var id int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO boards
				(slug, name, description, network_id, area_tag,
				 read_perms, post_perms, admin_perms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,
			spec.Slug, spec.Name, spec.Description, net.ID, areaTag,
			spec.ReadMinLevel, spec.PostMinLevel, spec.AdminMinLevel,
		)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("boards: create: %w", err)
	}
	return id, nil
}

// DeleteBoard removes a board. Refuses if any posts reference it.
func (s *Service) DeleteBoard(ctx context.Context, slug string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM board_posts WHERE board_id =
				(SELECT id FROM boards WHERE slug = ?)`, slug).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("boards: %q has %d posts; refuse to delete", slug, n)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM boards WHERE slug = ?`, slug)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetBoard returns a board by slug.
func (s *Service) GetBoard(ctx context.Context, slug string) (Board, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT id, network_id, slug, name, description, area_tag,
		       read_perms, post_perms, admin_perms
		FROM boards WHERE slug = ?
	`, slug)
	var b Board
	err := row.Scan(&b.ID, &b.NetworkID, &b.Slug, &b.Name, &b.Description,
		&b.AreaTag, &b.ReadMinLevel, &b.PostMinLevel, &b.AdminMinLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return Board{}, ErrNotFound
	}
	return b, err
}

// ListBoards returns every board the account is permitted to read.
func (s *Service) ListBoards(ctx context.Context, acc *auth.Account) ([]Board, error) {
	lvl := levelOf(acc)
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT id, network_id, slug, name, description, area_tag,
		       read_perms, post_perms, admin_perms
		FROM boards
		WHERE read_perms <= ?
		ORDER BY slug
	`, lvl)
	if err != nil {
		return nil, fmt.Errorf("boards: list: %w", err)
	}
	defer rows.Close()
	var out []Board
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.ID, &b.NetworkID, &b.Slug, &b.Name, &b.Description,
			&b.AreaTag, &b.ReadMinLevel, &b.PostMinLevel, &b.AdminMinLevel); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Post starts a new thread on the given board.
func (s *Service) Post(ctx context.Context, boardSlug string, author *auth.Account, subject, body string) (int64, error) {
	return s.post(ctx, boardSlug, author, subject, body, "" /* no parent */, 0)
}

// Reply continues an existing thread, anchored by parentID.
func (s *Service) Reply(ctx context.Context, boardSlug string, author *auth.Account, parentID int64, subject, body string) (int64, error) {
	// Look up parent MSGID + check it's on the same board.
	var parentMSGID string
	var parentBoard int64
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT msgid, board_id FROM board_posts WHERE id = ?`,
		parentID).Scan(&parentMSGID, &parentBoard)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, fmt.Errorf("boards: reply: lookup parent: %w", err)
	}
	b, err := s.GetBoard(ctx, boardSlug)
	if err != nil {
		return 0, err
	}
	if parentBoard != b.ID {
		return 0, fmt.Errorf("boards: parent post %d not on board %q", parentID, boardSlug)
	}
	return s.post(ctx, boardSlug, author, subject, body, parentMSGID, parentBoard)
}

// ReplyToMSGID lets a caller post a reply whose parent is referenced by
// MSGID rather than local id. Intended for FTN ingress code paths; in
// M6 it also enables the orphan-handling unit test.
func (s *Service) ReplyToMSGID(ctx context.Context, boardSlug string, author *auth.Account, parentMSGID, subject, body string) (int64, error) {
	return s.post(ctx, boardSlug, author, subject, body, parentMSGID, 0)
}

// post is the shared insertion path for Post, Reply, and ReplyToMSGID.
func (s *Service) post(ctx context.Context, boardSlug string, author *auth.Account, subject, body, replyToMSGID string, _ int64) (int64, error) {
	if subject == "" || body == "" {
		return 0, errors.New("boards: subject and body required")
	}
	b, err := s.GetBoard(ctx, boardSlug)
	if err != nil {
		return 0, err
	}
	if levelOf(author) < b.PostMinLevel {
		return 0, ErrForbidden
	}
	// Resolve the board's network and stamp a MSGID for it.
	var net ftnnetworks.Network
	if err := s.db.Read().QueryRowContext(ctx,
		`SELECT id, slug, name, domain, our_addr, is_default
		 FROM ftn_networks WHERE id = ?`, b.NetworkID).Scan(
		&net.ID, &net.Slug, &net.Name, &net.Domain, &net.OurAddr, new(int)); err != nil {
		return 0, fmt.Errorf("boards: fetch network: %w", err)
	}
	origAddr := net.OrigAddr()
	m, err := s.issuer.Issue(ctx, origAddr)
	if err != nil {
		return 0, fmt.Errorf("boards: issue msgid: %w", err)
	}

	// Resolve thread_root_msgid by walking the REPLY chain.
	threadRoot := m.String()
	var replyToCol sql.NullString
	if replyToMSGID != "" {
		replyToCol = sql.NullString{String: replyToMSGID, Valid: true}
		var parentRoot string
		err := s.db.Read().QueryRowContext(ctx,
			`SELECT thread_root_msgid FROM board_posts WHERE msgid = ?`, replyToMSGID).Scan(&parentRoot)
		if err == nil {
			threadRoot = parentRoot
		}
		// Orphan (parent unseen): leave threadRoot = own msgid.
		// TODO(M-FTN): reconciliation pass when the parent arrives via ingress.
	}

	_, tzOffset := time.Now().Zone()
	tzMinutes := tzOffset / 60

	tearline := nullStr(s.opts.Tearline)
	originLine := nullStr(fmt.Sprintf("* Origin: %s (%s)",
		fallback(s.opts.ServerName, "Wintermute"), net.OurAddr))
	seenBy := nullStr(net.OurAddr)
	path := nullStr(net.OurAddr)
	areaTag := b.AreaTag

	var authorID sql.NullInt64
	authorName := "system"
	if author != nil {
		authorID = sql.NullInt64{Int64: author.ID, Valid: true}
		authorName = author.Username
	}

	var newID int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO board_posts (
				board_id, network_id, author_id, author_name,
				msgid, reply_to_msgid, thread_root_msgid, origin_addr,
				area_tag, attributes, charset, pid, tz_offset,
				tearline, origin_line, seen_by, path,
				subject, body, posted_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))
		`,
			b.ID, net.ID, authorID, authorName,
			m.String(), replyToCol, threadRoot, origAddr,
			areaTag, 0, "UTF-8 4", s.opts.PID, tzMinutes,
			tearline, originLine, seenBy, path,
			subject, body,
		)
		if err != nil {
			return err
		}
		newID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("boards: insert: %w", err)
	}
	return newID, nil
}

// ListThreads returns a per-thread summary for every thread on the board,
// newest activity (newest post in the thread) first. The Unread flag is
// computed against acc's board_reads rows.
func (s *Service) ListThreads(ctx context.Context, boardSlug string, acc *auth.Account) ([]ThreadSummary, error) {
	b, err := s.GetBoard(ctx, boardSlug)
	if err != nil {
		return nil, err
	}
	if levelOf(acc) < b.ReadMinLevel {
		return nil, ErrForbidden
	}
	rows, err := s.db.Read().QueryContext(ctx, `
		WITH thread AS (
			SELECT thread_root_msgid,
			       MAX(posted_at) AS latest,
			       MAX(bp.id)     AS latest_id,
			       COUNT(*)       AS post_count,
			       SUM(CASE WHEN br.read_at IS NULL THEN 1 ELSE 0 END) AS unread
			FROM board_posts bp
			LEFT JOIN board_reads br
			  ON br.post_id = bp.id AND br.account_id = ?
			WHERE bp.board_id = ?
			GROUP BY thread_root_msgid
		)
		SELECT t.thread_root_msgid, root.id, root.subject, root.author_name,
		       t.latest, t.post_count, t.unread
		FROM thread t
		JOIN board_posts root
		  ON root.msgid = t.thread_root_msgid AND root.board_id = ?
		ORDER BY t.latest DESC, t.latest_id DESC
	`, accountID(acc), b.ID, b.ID)
	if err != nil {
		return nil, fmt.Errorf("boards: list threads: %w", err)
	}
	defer rows.Close()
	var out []ThreadSummary
	for rows.Next() {
		var (
			ts        ThreadSummary
			latest    int64
			postCount int
			unread    int
		)
		if err := rows.Scan(&ts.RootMSGID, &ts.RootPostID, &ts.Subject, &ts.AuthorName,
			&latest, &postCount, &unread); err != nil {
			return nil, err
		}
		ts.Latest = time.Unix(latest, 0).UTC()
		ts.ReplyCount = postCount - 1
		ts.Unread = unread > 0
		out = append(out, ts)
	}
	return out, rows.Err()
}

// ListThread returns every post in the thread containing postID, ordered
// oldest-first.
func (s *Service) ListThread(ctx context.Context, boardSlug string, postID int64, acc *auth.Account) ([]Post, error) {
	b, err := s.GetBoard(ctx, boardSlug)
	if err != nil {
		return nil, err
	}
	if levelOf(acc) < b.ReadMinLevel {
		return nil, ErrForbidden
	}
	var root string
	err = s.db.Read().QueryRowContext(ctx,
		`SELECT thread_root_msgid FROM board_posts WHERE id = ? AND board_id = ?`,
		postID, b.ID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT `+postColumns+` FROM board_posts
		WHERE thread_root_msgid = ? AND board_id = ?
		ORDER BY posted_at ASC, id ASC
	`, root, b.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReadPost returns a single post and records that acc has read it.
func (s *Service) ReadPost(ctx context.Context, boardSlug string, postID int64, acc *auth.Account) (Post, error) {
	b, err := s.GetBoard(ctx, boardSlug)
	if err != nil {
		return Post{}, err
	}
	if levelOf(acc) < b.ReadMinLevel {
		return Post{}, ErrForbidden
	}
	row := s.db.Read().QueryRowContext(ctx,
		`SELECT `+postColumns+` FROM board_posts WHERE id = ? AND board_id = ?`,
		postID, b.ID)
	p, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Post{}, ErrNotFound
	} else if err != nil {
		return Post{}, err
	}
	if acc != nil {
		err = s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO board_reads(account_id, post_id, read_at)
				VALUES (?, ?, strftime('%s','now'))
				ON CONFLICT DO NOTHING
			`, acc.ID, postID)
			return err
		})
		if err != nil {
			return p, fmt.Errorf("boards: mark read: %w", err)
		}
	}
	return p, nil
}

// CatchUp marks every post in the board as read for acc.
func (s *Service) CatchUp(ctx context.Context, boardSlug string, acc *auth.Account) error {
	if acc == nil {
		return nil
	}
	b, err := s.GetBoard(ctx, boardSlug)
	if err != nil {
		return err
	}
	if levelOf(acc) < b.ReadMinLevel {
		return ErrForbidden
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO board_reads(account_id, post_id, read_at)
			SELECT ?, id, strftime('%s','now') FROM board_posts WHERE board_id = ?
			ON CONFLICT DO NOTHING
		`, acc.ID, b.ID)
		return err
	})
}

const postColumns = `
	id, board_id, network_id, author_id, author_name,
	msgid, reply_to_msgid, thread_root_msgid, origin_addr,
	area_tag, attributes, charset, pid, tz_offset,
	tearline, origin_line, seen_by, path, kludges,
	subject, body, posted_at
`

type scanner interface {
	Scan(dest ...any) error
}

func scanPost(s scanner) (Post, error) {
	var (
		p     Post
		ts    int64
		attrs int64
	)
	err := s.Scan(
		&p.ID, &p.BoardID, &p.NetworkID, &p.AuthorID, &p.AuthorName,
		&p.MSGID, &p.ReplyToMSGID, &p.ThreadRootMSGID, &p.OriginAddr,
		&p.AreaTag, &attrs, &p.Charset, &p.PID, &p.TZOffset,
		&p.Tearline, &p.OriginLine, &p.SeenBy, &p.Path, &p.Kludges,
		&p.Subject, &p.Body, &ts,
	)
	if err != nil {
		return Post{}, err
	}
	p.Attributes = uint16(attrs)
	p.PostedAt = time.Unix(ts, 0).UTC()
	return p, nil
}

func levelOf(acc *auth.Account) int {
	if acc == nil {
		return LevelOpen
	}
	switch acc.AccessLevel {
	case auth.AccessAdmin:
		return LevelAdmin
	case auth.AccessBuilder:
		return LevelBuilder
	case auth.AccessPlayer:
		return LevelPlayer
	}
	return LevelOpen
}

func accountID(acc *auth.Account) int64 {
	if acc == nil {
		return 0
	}
	return acc.ID
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

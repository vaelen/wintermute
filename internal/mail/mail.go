// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package mail

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

// Sentinel errors returned by Service methods.
var (
	ErrRecipientNotFound = errors.New("mail: recipient not found")
	ErrParentNotFound    = errors.New("mail: parent message not found")
	ErrEmptyBody         = errors.New("mail: empty body")
	ErrEmptySubject      = errors.New("mail: empty subject")
	ErrNotFound          = errors.New("mail: not found")
)

// Mail is one row of the mail table.
type Mail struct {
	ID           int64
	NetworkID    int64
	FromID       sql.NullInt64
	ToID         sql.NullInt64
	FromName     string
	ToName       string
	FromAddr     string
	ToAddr       string
	MSGID        string
	ReplyToMSGID sql.NullString
	OriginAddr   string
	Attributes   uint16
	Charset      string
	PID          sql.NullString
	TZOffset     sql.NullInt64
	Kludges      sql.NullString
	Subject      string
	Body         string
	SentAt       time.Time
	ReadAt       sql.NullTime
}

// Service is the mail subsystem.
type Service struct {
	db     *store.DB
	issuer *msgid.Issuer
	pid    string
}

// NewService constructs a Service. pid is the FSC-0046 PID kludge value
// stamped on every locally-originated message (e.g. "Wintermute/1.2.3").
func NewService(db *store.DB, issuer *msgid.Issuer, pid string) *Service {
	return &Service{db: db, issuer: issuer, pid: pid}
}

// Send delivers a message from `from` (a local account) to a local
// account named `toName`. Uses the `local` ftn network. If replyTo is
// non-empty it must be the MSGID of an existing local mail message;
// the new message links to it via reply_to_msgid.
func (s *Service) Send(ctx context.Context, from *auth.Account, toName, subject, body, replyTo string) (int64, error) {
	if subject == "" {
		return 0, ErrEmptySubject
	}
	if body == "" {
		return 0, ErrEmptyBody
	}

	// Resolve recipient by username.
	var toID int64
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT id FROM accounts WHERE username = ?`, toName).Scan(&toID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: %q", ErrRecipientNotFound, toName)
	} else if err != nil {
		return 0, fmt.Errorf("mail: resolve recipient: %w", err)
	}

	// Validate reply parent exists (locally).
	if replyTo != "" {
		var n int
		err := s.db.Read().QueryRowContext(ctx,
			`SELECT 1 FROM mail WHERE msgid = ?`, replyTo).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: %q", ErrParentNotFound, replyTo)
		} else if err != nil {
			return 0, fmt.Errorf("mail: lookup parent: %w", err)
		}
	}

	// Mail between two local accounts always uses the `local` network.
	local, err := ftnnetworks.Get(ctx, s.db, "local")
	if err != nil {
		return 0, fmt.Errorf("mail: get local network: %w", err)
	}
	origAddr := local.OrigAddr()

	m, err := s.issuer.Issue(ctx, origAddr)
	if err != nil {
		return 0, fmt.Errorf("mail: issue MSGID: %w", err)
	}

	_, tzOffset := time.Now().Zone() // seconds east of UTC
	tzMinutes := tzOffset / 60

	var replyToCol sql.NullString
	if replyTo != "" {
		replyToCol = sql.NullString{String: replyTo, Valid: true}
	}

	var newID int64
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO mail (
				network_id, from_id, to_id,
				from_name, to_name, from_addr, to_addr,
				msgid, reply_to_msgid, origin_addr,
				attributes, charset, pid, tz_offset,
				subject, body, sent_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s','now'))
		`,
			local.ID, from.ID, toID,
			from.Username, toName, origAddr, origAddr,
			m.String(), replyToCol, origAddr,
			0, "UTF-8 4", s.pid, tzMinutes,
			subject, body,
		)
		if err != nil {
			return err
		}
		newID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("mail: insert: %w", err)
	}
	return newID, nil
}

// Inbox returns every mail addressed to accountID, newest first. The
// returned rows include the metadata needed to render a listing; Body
// is included since it's cheap and callers usually need it.
func (s *Service) Inbox(ctx context.Context, accountID int64) ([]Mail, error) {
	rows, err := s.db.Read().QueryContext(ctx, `
		SELECT `+mailColumns+` FROM mail
		WHERE to_id = ?
		ORDER BY sent_at DESC, id DESC
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("mail: inbox: %w", err)
	}
	defer rows.Close()
	var out []Mail
	for rows.Next() {
		m, err := scanMail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Read returns the mail with the given id provided it is addressed to
// accountID. It marks read_at on the row if it wasn't already set.
func (s *Service) Read(ctx context.Context, id, accountID int64) (Mail, error) {
	row := s.db.Read().QueryRowContext(ctx, `
		SELECT `+mailColumns+` FROM mail
		WHERE id = ? AND to_id = ?
	`, id, accountID)
	m, err := scanMail(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Mail{}, ErrNotFound
	} else if err != nil {
		return Mail{}, fmt.Errorf("mail: read: %w", err)
	}
	if !m.ReadAt.Valid {
		err = s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE mail SET read_at = strftime('%s','now') WHERE id = ? AND read_at IS NULL`,
				id)
			return err
		})
		if err != nil {
			return m, fmt.Errorf("mail: mark read: %w", err)
		}
		m.ReadAt = sql.NullTime{Time: time.Now(), Valid: true}
	}
	return m, nil
}

// Delete removes the mail with the given id if it is addressed to accountID.
func (s *Service) Delete(ctx context.Context, id, accountID int64) error {
	var affected int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM mail WHERE id = ? AND to_id = ?`, id, accountID)
		if err != nil {
			return err
		}
		affected, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return fmt.Errorf("mail: delete: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// UnreadCount returns the number of unread mails addressed to accountID.
func (s *Service) UnreadCount(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mail WHERE to_id = ? AND read_at IS NULL`,
		accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mail: unread count: %w", err)
	}
	return n, nil
}

const mailColumns = `
	id, network_id, from_id, to_id,
	from_name, to_name, from_addr, to_addr,
	msgid, reply_to_msgid, origin_addr,
	attributes, charset, pid, tz_offset, kludges,
	subject, body, sent_at, read_at
`

type scanner interface {
	Scan(dest ...any) error
}

func scanMail(s scanner) (Mail, error) {
	var (
		m      Mail
		sent   int64
		readAt sql.NullInt64
		attrs  int64
	)
	err := s.Scan(
		&m.ID, &m.NetworkID, &m.FromID, &m.ToID,
		&m.FromName, &m.ToName, &m.FromAddr, &m.ToAddr,
		&m.MSGID, &m.ReplyToMSGID, &m.OriginAddr,
		&attrs, &m.Charset, &m.PID, &m.TZOffset, &m.Kludges,
		&m.Subject, &m.Body, &sent, &readAt,
	)
	if err != nil {
		return Mail{}, err
	}
	m.Attributes = uint16(attrs)
	m.SentAt = time.Unix(sent, 0).UTC()
	if readAt.Valid {
		m.ReadAt = sql.NullTime{Time: time.Unix(readAt.Int64, 0).UTC(), Valid: true}
	}
	return m, nil
}

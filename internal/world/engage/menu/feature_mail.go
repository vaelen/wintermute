// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// pageSize is the fixed window size for paginated submenu lists. N/P
// page through the list; PgUp/PgDn-style scrolling is out of scope for
// M6.3.
const pageSize = 15

// ---------------------------------------------------------------------------
// mailInbox: paginated list of inbox messages.

type mailInbox struct {
	page int // 0-based
}

func (s mailInbox) render(h *Handler, p *engage.Participant) string {
	rows := []Row{{Blank: true}}
	box, err := loadInbox(h, p)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to read inbox: " + err.Error() + ")"})
	case len(box) == 0:
		rows = append(rows, Row{Label: "(inbox is empty)"})
	default:
		start := s.page * pageSize
		end := start + pageSize
		if end > len(box) {
			end = len(box)
		}
		if start >= len(box) {
			start, end = 0, 0
		}
		for i := start; i < end; i++ {
			m := box[i]
			unread := " "
			if !m.ReadAt.Valid {
				unread = "*"
			}
			label := fmt.Sprintf("%s %s — %s", unread,
				truncRune(m.FromName, 12),
				truncRune(m.Subject, 30))
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    label,
			})
		}
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "C)", Label: "Compose"},
	)
	if s.page > 0 {
		rows = append(rows, Row{Selector: "P)", Label: "Previous page"})
	}
	if (s.page+1)*pageSize < len(box) {
		rows = append(rows, Row{Selector: "N)", Label: "Next page"})
	}
	rows = append(rows,
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	f := Frame{Width: h.width0(), Title: "Mail", Rows: rows}
	return f.String() + "Select: "
}

func (s mailInbox) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, mainMenu{})
		return
	case "c", "compose":
		h.transition(p, mailComposeRecipient{parent: s})
		return
	case "n", "next":
		h.transition(p, mailInbox{page: s.page + 1})
		return
	case "p", "prev", "previous":
		next := s.page - 1
		if next < 0 {
			next = 0
		}
		h.transition(p, mailInbox{page: next})
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		box, berr := loadInbox(h, p)
		if berr == nil && n >= 1 && n <= len(box) {
			h.transition(p, mailRead{parent: s, mailID: box[n-1].ID})
			return
		}
	}
	h.redraw(p)
}

// loadInbox resolves deps + account and returns the participant's inbox.
// Returns (nil, nil) when deps or account lookup are not configured.
func loadInbox(h *Handler, p *engage.Participant) ([]mail.Mail, error) {
	d, acc, ok := mailReady(h, p)
	if !ok {
		return nil, nil
	}
	return d.Mail.Inbox(h.ctx(), acc.ID)
}

// mailReady resolves the deps + participant's account once. ok=false
// when the mail service or account lookup is missing/failed.
func mailReady(h *Handler, p *engage.Participant) (*engage.TerminalDeps, *auth.Account, bool) {
	h.mu.Lock()
	d := h.deps
	h.mu.Unlock()
	if d == nil || d.Mail == nil || d.AccountFor == nil || p == nil {
		return nil, nil, false
	}
	acc, err := d.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		return nil, nil, false
	}
	return d, acc, true
}

// ---------------------------------------------------------------------------
// mailRead: a single message with Reply / Delete / Back controls.

type mailRead struct {
	parent mailInbox
	mailID int64
}

func (s mailRead) render(h *Handler, p *engage.Participant) string {
	d, acc, ok := mailReady(h, p)
	if !ok {
		return placeholderState{title: "Mail"}.render(h, p)
	}
	m, err := d.Mail.Read(h.ctx(), s.mailID, acc.ID)
	rows := []Row{{Blank: true}}
	if err != nil {
		rows = append(rows, Row{Label: "(unable to read: " + err.Error() + ")"})
	} else {
		rows = append(rows,
			Row{Label: "From:    " + m.FromName},
			Row{Label: "Subject: " + truncRune(m.Subject, 50)},
			Row{Label: "Date:    " + m.SentAt.Format("2006-01-02 15:04")},
			Row{Blank: true},
		)
		for _, ln := range splitBodyLines(m.Body, h.width0()-6) {
			rows = append(rows, Row{Label: ln})
		}
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "R)", Label: "Reply"},
		Row{Selector: "D)", Label: "Delete"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	f := Frame{Width: h.width0(), Title: "Mail — Message", Rows: rows}
	return f.String() + "Select: "
}

func (s mailRead) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "b", "back", "..", "":
		h.transition(p, s.parent)
		return
	case "d", "delete":
		if d, acc, ok := mailReady(h, p); ok {
			_ = d.Mail.Delete(h.ctx(), s.mailID, acc.ID)
		}
		h.transition(p, s.parent)
		return
	case "r", "reply":
		if d, acc, ok := mailReady(h, p); ok {
			m, err := d.Mail.Read(h.ctx(), s.mailID, acc.ID)
			if err == nil {
				subj := m.Subject
				if !strings.HasPrefix(strings.ToLower(subj), "re:") {
					subj = "Re: " + subj
				}
				h.transition(p, mailComposeBody{
					parent:  s.parent,
					to:      m.FromName,
					subject: subj,
					replyTo: m.MSGID,
				})
				return
			}
		}
		h.redraw(p)
		return
	}
	h.redraw(p)
}

// ---------------------------------------------------------------------------
// compose: recipient → subject → body (paste mode)

type mailComposeRecipient struct{ parent mailInbox }

func (s mailComposeRecipient) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Compose new mail."},
		{Label: "Type a recipient username, or /abort to cancel."},
		{Blank: true},
	}
	f := Frame{Width: h.width0(), Title: "Mail — Compose (Recipient)", Rows: rows}
	return f.String() + "To: "
}

func (s mailComposeRecipient) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, s.parent)
		return
	}
	h.transition(p, mailComposeSubject{parent: s.parent, to: trimmed})
}

type mailComposeSubject struct {
	parent mailInbox
	to     string
}

func (s mailComposeSubject) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "To: " + s.to},
		{Label: "Type a subject, or /abort to cancel."},
		{Blank: true},
	}
	f := Frame{Width: h.width0(), Title: "Mail — Compose (Subject)", Rows: rows}
	return f.String() + "Subject: "
}

func (s mailComposeSubject) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, s.parent)
		return
	}
	h.transition(p, mailComposeBody{parent: s.parent, to: s.to, subject: trimmed})
}

type mailComposeBody struct {
	parent  mailInbox
	to      string
	subject string
	replyTo string
	body    []string
}

func (s mailComposeBody) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "To:      " + s.to},
		{Label: "Subject: " + truncRune(s.subject, 50)},
		{Blank: true},
		{Label: "Enter your message. End with a single line containing only `.`"},
		{Label: "or /abort to cancel."},
		{Blank: true},
	}
	f := Frame{Width: h.width0(), Title: "Mail — Compose (Body)", Rows: rows}
	return f.String() + ".> "
}

func (s mailComposeBody) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case "/abort":
		h.transition(p, s.parent)
		return
	case ".":
		d, acc, ok := mailReady(h, p)
		if !ok {
			h.transition(p, s.parent)
			return
		}
		body := strings.Join(s.body, "\n")
		if _, err := d.Mail.Send(h.ctx(), acc, s.to, s.subject, body, s.replyTo); err != nil {
			_ = p.Write(fmt.Sprintf("mail: %v\r\n", err))
		}
		h.transition(p, s.parent)
		return
	}
	// Body line — append silently and keep paste-mode prompt.
	s.body = append(s.body, line)
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
	_ = p.Write(".> ")
}

// ---------------------------------------------------------------------------
// helpers

// truncRune truncates s to at most n runes, appending an ellipsis when
// truncation occurred. Mirrors the existing terminal handler's helper.
func truncRune(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count >= n {
			return s[:i-1] + "…"
		}
		count++
	}
	return s
}

// splitBodyLines breaks body into terminal-friendly lines, naively
// truncating individual lines longer than max. M6.3 does not word-wrap.
func splitBodyLines(body string, max int) []string {
	if max <= 0 {
		max = 40
	}
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		raw = strings.TrimRight(raw, "\r")
		for len(raw) > max {
			out = append(out, raw[:max])
			raw = raw[max:]
		}
		out = append(out, raw)
	}
	return out
}

// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// ---------------------------------------------------------------------------
// boardsList: top-level board listing.

type boardsList struct {
	page int
}

func (s boardsList) render(h *Handler, p *engage.Participant) string {
	rows := []Row{{Blank: true}}
	bs, err := loadBoards(h, p)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to list boards: " + err.Error() + ")"})
	case len(bs) == 0:
		rows = append(rows, Row{Label: "(no visible boards)"})
	default:
		start := s.page * pageSize
		end := start + pageSize
		if end > len(bs) {
			end = len(bs)
		}
		if start >= len(bs) {
			start, end = 0, 0
		}
		for i := start; i < end; i++ {
			b := bs[i]
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%s — %s", truncRune(b.Slug, 16), truncRune(b.Name, 30)),
			})
		}
	}
	rows = append(rows, Row{Blank: true})
	if s.page > 0 {
		rows = append(rows, Row{Selector: "P)", Label: "Previous page"})
	}
	if (s.page+1)*pageSize < len(bs) {
		rows = append(rows, Row{Selector: "N)", Label: "Next page"})
	}
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	f := Frame{Width: h.width0(), Title: "Message Boards", Rows: rows}
	return f.String() + "Select: "
}

func (s boardsList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, mainMenu{})
		return
	case "n", "next":
		h.transition(p, boardsList{page: s.page + 1})
		return
	case "p", "prev", "previous":
		next := s.page - 1
		if next < 0 {
			next = 0
		}
		h.transition(p, boardsList{page: next})
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		bs, berr := loadBoards(h, p)
		if berr == nil && n >= 1 && n <= len(bs) {
			h.transition(p, boardThreads{parent: s, board: bs[n-1].Slug})
			return
		}
	}
	h.redraw(p)
}

func loadBoards(h *Handler, p *engage.Participant) ([]boards.Board, error) {
	d, acc, ok := boardsReady(h, p)
	if !ok {
		return nil, nil
	}
	return d.Boards.ListBoards(h.ctx(), acc)
}

func boardsReady(h *Handler, p *engage.Participant) (*engage.TerminalDeps, *auth.Account, bool) {
	h.mu.Lock()
	d := h.deps
	h.mu.Unlock()
	if d == nil || d.Boards == nil || d.AccountFor == nil || p == nil {
		return nil, nil, false
	}
	acc, err := d.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		return nil, nil, false
	}
	return d, acc, true
}

// ---------------------------------------------------------------------------
// boardThreads: list of threads in a single board.

type boardThreads struct {
	parent boardsList
	board  string
	page   int
}

func (s boardThreads) render(h *Handler, p *engage.Participant) string {
	rows := []Row{{Blank: true}}
	threads, err := loadThreads(h, p, s.board)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to list threads: " + err.Error() + ")"})
	case len(threads) == 0:
		rows = append(rows, Row{Label: "(no threads)"})
	default:
		start := s.page * pageSize
		end := start + pageSize
		if end > len(threads) {
			end = len(threads)
		}
		if start >= len(threads) {
			start, end = 0, 0
		}
		for i := start; i < end; i++ {
			th := threads[i]
			unread := " "
			if th.Unread {
				unread = "*"
			}
			label := fmt.Sprintf("%s %s — %s (%d)", unread,
				truncRune(th.AuthorName, 12),
				truncRune(th.Subject, 28),
				th.ReplyCount)
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    label,
			})
		}
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "N)", Label: "New post"},
		Row{Selector: "C)", Label: "Catch up"},
	)
	if s.page > 0 {
		rows = append(rows, Row{Selector: "P)", Label: "Previous page"})
	}
	if (s.page+1)*pageSize < len(threads) {
		rows = append(rows, Row{Selector: ">)", Label: "Next page"})
	}
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	f := Frame{Width: h.width0(), Title: "Boards — " + s.board, Rows: rows}
	return f.String() + "Select: "
}

func (s boardThreads) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, s.parent)
		return
	case "n", "new":
		h.transition(p, boardComposeSubject{parent: s, board: s.board})
		return
	case "c", "catch", "catchup", "catch up":
		if d, acc, ok := boardsReady(h, p); ok {
			_ = d.Boards.CatchUp(h.ctx(), s.board, acc)
		}
		h.transition(p, s)
		return
	case ">":
		h.transition(p, boardThreads{parent: s.parent, board: s.board, page: s.page + 1})
		return
	case "p", "prev", "previous":
		next := s.page - 1
		if next < 0 {
			next = 0
		}
		h.transition(p, boardThreads{parent: s.parent, board: s.board, page: next})
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		threads, terr := loadThreads(h, p, s.board)
		if terr == nil && n >= 1 && n <= len(threads) {
			h.transition(p, boardThreadView{
				parent: s, board: s.board, rootID: threads[n-1].RootPostID,
			})
			return
		}
	}
	h.redraw(p)
}

func loadThreads(h *Handler, p *engage.Participant, board string) ([]boards.ThreadSummary, error) {
	d, acc, ok := boardsReady(h, p)
	if !ok {
		return nil, nil
	}
	return d.Boards.ListThreads(h.ctx(), board, acc)
}

// ---------------------------------------------------------------------------
// boardThreadView: a single thread's posts with Reply / Back controls.

type boardThreadView struct {
	parent boardThreads
	board  string
	rootID int64
}

func (s boardThreadView) render(h *Handler, p *engage.Participant) string {
	rows := []Row{{Blank: true}}
	posts, err := loadThread(h, p, s.board, s.rootID)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to read thread: " + err.Error() + ")"})
	case len(posts) == 0:
		rows = append(rows, Row{Label: "(thread is empty)"})
	default:
		for _, post := range posts {
			rows = append(rows,
				Row{Label: fmt.Sprintf("[%d] %s — %s", post.ID,
					truncRune(post.AuthorName, 12),
					post.PostedAt.Format("2006-01-02 15:04"))},
				Row{Label: "Subject: " + truncRune(post.Subject, 50)},
				Row{Blank: true},
			)
			for _, ln := range splitBodyLines(post.Body, h.width0()-6) {
				rows = append(rows, Row{Label: ln})
			}
			rows = append(rows, Row{Label: "---"})
		}
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "R)", Label: "Reply"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	f := Frame{Width: h.width0(), Title: "Boards — " + s.board + " — thread", Rows: rows}
	return f.String() + "Select: "
}

func (s boardThreadView) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "b", "back", "..", "":
		h.transition(p, s.parent)
		return
	case "r", "reply":
		h.transition(p, boardComposeSubject{
			parent: s.parent, board: s.board, replyTo: s.rootID, isReply: true,
		})
		return
	}
	h.redraw(p)
}

func loadThread(h *Handler, p *engage.Participant, board string, id int64) ([]boards.Post, error) {
	d, acc, ok := boardsReady(h, p)
	if !ok {
		return nil, nil
	}
	return d.Boards.ListThread(h.ctx(), board, id, acc)
}

// ---------------------------------------------------------------------------
// board compose: subject → body (paste mode). Used for both new post and
// reply. When isReply is true, replyTo is the parent root post ID and
// the parent subject is fetched to prefix "Re:".

type boardComposeSubject struct {
	parent  boardThreads
	board   string
	replyTo int64
	isReply bool
}

func (s boardComposeSubject) render(h *Handler, _ *engage.Participant) string {
	title := "Boards — " + s.board + " — New Post"
	if s.isReply {
		title = "Boards — " + s.board + " — Reply"
	}
	rows := []Row{
		{Blank: true},
		{Label: "Type a subject, or /abort to cancel."},
		{Blank: true},
	}
	f := Frame{Width: h.width0(), Title: title, Rows: rows}
	return f.String() + "Subject: "
}

func (s boardComposeSubject) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, s.parent)
		return
	}
	subject := trimmed
	if s.isReply && !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	h.transition(p, boardComposeBody{
		parent:  s.parent,
		board:   s.board,
		replyTo: s.replyTo,
		isReply: s.isReply,
		subject: subject,
	})
}

type boardComposeBody struct {
	parent  boardThreads
	board   string
	replyTo int64
	isReply bool
	subject string
	body    []string
}

func (s boardComposeBody) render(h *Handler, _ *engage.Participant) string {
	title := "Boards — " + s.board + " — Body"
	rows := []Row{
		{Blank: true},
		{Label: "Subject: " + truncRune(s.subject, 50)},
		{Blank: true},
		{Label: "Enter your message. End with a single line containing only `.`"},
		{Label: "or /abort to cancel."},
		{Blank: true},
	}
	f := Frame{Width: h.width0(), Title: title, Rows: rows}
	return f.String() + ".> "
}

func (s boardComposeBody) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case "/abort":
		h.transition(p, s.parent)
		return
	case ".":
		d, acc, ok := boardsReady(h, p)
		if !ok {
			h.transition(p, s.parent)
			return
		}
		body := strings.Join(s.body, "\n")
		var err error
		if s.isReply {
			_, err = d.Boards.Reply(h.ctx(), s.board, acc, s.replyTo, s.subject, body)
		} else {
			_, err = d.Boards.Post(h.ctx(), s.board, acc, s.subject, body)
		}
		if err != nil {
			_ = p.Write(fmt.Sprintf("boards: %v\r\n", err))
		}
		h.transition(p, s.parent)
		return
	}
	s.body = append(s.body, line)
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
	_ = p.Write(".> ")
}

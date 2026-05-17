// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/world"
)

// terminalCommands lists every command available inside a terminal
// engagement.
var terminalCommands = []string{
	"mail", "bb", "bbread", "bbthread", "bbpost", "bbreply", "bbcatchup",
	"upload", "download", "help",
}

// TerminalDeps bundles the services a terminal handler may call into.
// Any nil field falls back to the "not yet implemented" stub for the
// corresponding commands.
type TerminalDeps struct {
	// Mail is the mail service backing the `mail` family of commands.
	Mail *mail.Service
	// Boards backs the `bb*` family.
	Boards *boards.Service
	// Files + UploadURL back `upload` and `download`. UploadURL builds
	// the public URL a player should paste into curl for a given token,
	// e.g. https://host:port/upload/<token>.
	Files     *files.Service
	UploadURL func(token string) string
	DownloadURL func(token string) string

	// AccountFor maps a player's body ObjectID to its auth.Account.
	// Required when Mail/Boards/Files are non-nil.
	AccountFor func(world.ObjectID) (*auth.Account, error)
}

// TerminalHandler is the built-in handler for kind='terminal' hosts.
// Capacity-1 in M5.7; one in-flight paste compose per handler suffices.
type TerminalHandler struct {
	host    *Host
	close   func()
	deps    *TerminalDeps
	compose *composeState
}

type composeState struct {
	kind    string // "mail-send" | "mail-reply" | "bb-post" | "bb-reply"
	args    []string
	body    []string
	subject string
}

// NewTerminalHandler returns a fresh handler bound to host. onClose, if
// non-nil, is invoked from OnClose.
func NewTerminalHandler(host *Host, onClose func()) *TerminalHandler {
	return &TerminalHandler{host: host, close: onClose}
}

// SetDeps attaches the service dependencies. May be called after
// construction; nil restores the stub behaviour for every command.
func (h *TerminalHandler) SetDeps(d *TerminalDeps) {
	h.deps = d
}

func (h *TerminalHandler) OnOpen(p *Participant) {
	h.writePrompt(p)
}

// OnClose fires the optional close callback.
func (h *TerminalHandler) OnClose(_ *Participant, _ CloseReason) {
	if h.close != nil {
		h.close()
	}
}

// Handle dispatches a single line of terminal input.
func (h *TerminalHandler) Handle(p *Participant, line string) {
	// Paste-mode: every line goes to the in-flight compose until "."
	if h.compose != nil {
		h.handleComposeLine(p, line)
		return
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.writePrompt(p)
		return
	}
	cmd, rest := splitTerminalCmd(trimmed)
	switch strings.ToLower(cmd) {
	case "help":
		h.writeHelp(p)
	case "mail":
		h.handleMail(p, rest)
	case "bb":
		h.handleBoards(p, "bb", rest)
	case "bbread":
		h.handleBoards(p, "bbread", rest)
	case "bbthread":
		h.handleBoards(p, "bbthread", rest)
	case "bbpost":
		h.handleBoards(p, "bbpost", rest)
	case "bbreply":
		h.handleBoards(p, "bbreply", rest)
	case "bbcatchup":
		h.handleBoards(p, "bbcatchup", rest)
	case "upload":
		h.handleUpload(p, rest)
	case "download":
		h.handleDownload(p, rest)
	default:
		_ = p.Write("> command not recognized — try `help`.\r\n")
	}
	h.writePrompt(p)
}

// -- mail ---------------------------------------------------------------

func (h *TerminalHandler) handleMail(p *Participant, args string) {
	if h.deps == nil || h.deps.Mail == nil || h.deps.AccountFor == nil {
		_ = p.Write("mail: not yet implemented (M6).\r\n")
		return
	}
	ctx := context.Background()
	acc, err := h.deps.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		_ = p.Write("mail: cannot resolve account.\r\n")
		return
	}
	sub, rest := splitTerminalCmd(args)
	switch strings.ToLower(sub) {
	case "":
		h.writeInbox(p, ctx, acc)
	case "send":
		h.startMailSend(p, rest)
	case "read":
		h.readMail(p, ctx, acc, rest)
	case "reply":
		h.startMailReply(p, ctx, acc, rest)
	case "delete":
		h.deleteMail(p, ctx, acc, rest)
	default:
		_ = p.Write("mail: unknown subcommand. Try `mail` (inbox), `mail send <user> \"<subject>\"`, `mail read <id>`, `mail reply <id>`, `mail delete <id>`.\r\n")
	}
}

func (h *TerminalHandler) writeInbox(p *Participant, ctx context.Context, acc *auth.Account) {
	box, err := h.deps.Mail.Inbox(ctx, acc.ID)
	if err != nil {
		_ = p.Write(fmt.Sprintf("mail: %v\r\n", err))
		return
	}
	if len(box) == 0 {
		_ = p.Write("Inbox is empty.\r\n")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Inbox (%d message%s):\r\n", len(box), plural(len(box)))
	for _, m := range box {
		unread := " "
		if !m.ReadAt.Valid {
			unread = "*"
		}
		fmt.Fprintf(&b, "  %s [%d] %-12s  %s  %s\r\n",
			unread, m.ID, truncate(m.FromName, 12),
			m.SentAt.Format("2006-01-02 15:04"),
			truncate(m.Subject, 50))
	}
	_ = p.Write(b.String())
}

func (h *TerminalHandler) startMailSend(p *Participant, args string) {
	to, rest := splitTerminalCmd(args)
	if to == "" {
		_ = p.Write(`mail send: usage "mail send <user> \"<subject>\""` + "\r\n")
		return
	}
	subject, ok := parseQuotedSubject(rest)
	if !ok || subject == "" {
		_ = p.Write(`mail send: subject must be quoted, e.g. mail send bob "Hello"` + "\r\n")
		return
	}
	h.beginCompose(p, &composeState{
		kind: "mail-send", args: []string{to}, subject: subject,
	})
}

func (h *TerminalHandler) readMail(p *Participant, ctx context.Context, acc *auth.Account, idStr string) {
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil {
		_ = p.Write("mail read: numeric id required.\r\n")
		return
	}
	m, err := h.deps.Mail.Read(ctx, id, acc.ID)
	if err != nil {
		_ = p.Write(fmt.Sprintf("mail read: %v\r\n", err))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\nTo: %s\r\nDate: %s\r\nSubject: %s\r\nMSGID: %s\r\n",
		m.FromName, m.FromAddr, m.ToName,
		m.SentAt.Format(time.RFC1123Z), m.Subject, m.MSGID)
	if m.ReplyToMSGID.Valid {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", m.ReplyToMSGID.String)
	}
	b.WriteString("\r\n")
	b.WriteString(m.Body)
	if !strings.HasSuffix(m.Body, "\n") {
		b.WriteString("\r\n")
	}
	_ = p.Write(b.String())
}

func (h *TerminalHandler) startMailReply(p *Participant, ctx context.Context, acc *auth.Account, idStr string) {
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil {
		_ = p.Write("mail reply: numeric id required.\r\n")
		return
	}
	parent, err := h.deps.Mail.Read(ctx, id, acc.ID)
	if err != nil {
		_ = p.Write(fmt.Sprintf("mail reply: %v\r\n", err))
		return
	}
	subj := parent.Subject
	if !strings.HasPrefix(strings.ToLower(subj), "re:") {
		subj = "Re: " + subj
	}
	h.beginCompose(p, &composeState{
		kind: "mail-reply", args: []string{parent.FromName, parent.MSGID},
		subject: subj,
	})
}

func (h *TerminalHandler) deleteMail(p *Participant, ctx context.Context, acc *auth.Account, idStr string) {
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil {
		_ = p.Write("mail delete: numeric id required.\r\n")
		return
	}
	if err := h.deps.Mail.Delete(ctx, id, acc.ID); err != nil {
		_ = p.Write(fmt.Sprintf("mail delete: %v\r\n", err))
		return
	}
	_ = p.Write("Deleted.\r\n")
}

// -- boards -------------------------------------------------------------

func (h *TerminalHandler) handleBoards(p *Participant, cmd, args string) {
	if h.deps == nil || h.deps.Boards == nil || h.deps.AccountFor == nil {
		_ = p.Write(cmd + ": not yet implemented (M6).\r\n")
		return
	}
	ctx := context.Background()
	acc, err := h.deps.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		_ = p.Write(cmd + ": cannot resolve account.\r\n")
		return
	}
	switch cmd {
	case "bb":
		h.listBoardsCmd(p, ctx, acc)
	case "bbread":
		h.readBoard(p, ctx, acc, args)
	case "bbthread":
		h.readThread(p, ctx, acc, args)
	case "bbpost":
		h.startBoardPost(p, args)
	case "bbreply":
		h.startBoardReply(p, ctx, acc, args)
	case "bbcatchup":
		h.catchUpBoard(p, ctx, acc, args)
	}
}

func (h *TerminalHandler) listBoardsCmd(p *Participant, ctx context.Context, acc *auth.Account) {
	bs, err := h.deps.Boards.ListBoards(ctx, acc)
	if err != nil {
		_ = p.Write(fmt.Sprintf("bb: %v\r\n", err))
		return
	}
	if len(bs) == 0 {
		_ = p.Write("No boards visible.\r\n")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Boards (%d):\r\n", len(bs))
	for _, b := range bs {
		fmt.Fprintf(&sb, "  %-16s %s\r\n", b.Slug, b.Name)
	}
	_ = p.Write(sb.String())
}

func (h *TerminalHandler) readBoard(p *Participant, ctx context.Context, acc *auth.Account, args string) {
	board, idStr := splitTerminalCmd(args)
	if board == "" {
		_ = p.Write("bbread: usage `bbread <board> [<id>]`\r\n")
		return
	}
	if idStr != "" {
		// Single post.
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			_ = p.Write("bbread: numeric post id required.\r\n")
			return
		}
		post, err := h.deps.Boards.ReadPost(ctx, board, id, acc)
		if err != nil {
			_ = p.Write(fmt.Sprintf("bbread: %v\r\n", err))
			return
		}
		_ = p.Write(renderPost(post))
		return
	}
	threads, err := h.deps.Boards.ListThreads(ctx, board, acc)
	if err != nil {
		_ = p.Write(fmt.Sprintf("bbread: %v\r\n", err))
		return
	}
	if len(threads) == 0 {
		_ = p.Write("(empty)\r\n")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Threads in %s (%d):\r\n", board, len(threads))
	for _, th := range threads {
		marker := " "
		if th.Unread {
			marker = "*"
		}
		fmt.Fprintf(&sb, "  %s [%d] %-16s  %d repl%s  %s\r\n",
			marker, th.RootPostID, truncate(th.AuthorName, 16),
			th.ReplyCount, pluralReply(th.ReplyCount), truncate(th.Subject, 50))
	}
	_ = p.Write(sb.String())
}

func (h *TerminalHandler) readThread(p *Participant, ctx context.Context, acc *auth.Account, args string) {
	board, idStr := splitTerminalCmd(args)
	if board == "" || idStr == "" {
		_ = p.Write("bbthread: usage `bbthread <board> <id>`\r\n")
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = p.Write("bbthread: numeric id required.\r\n")
		return
	}
	posts, err := h.deps.Boards.ListThread(ctx, board, id, acc)
	if err != nil {
		_ = p.Write(fmt.Sprintf("bbthread: %v\r\n", err))
		return
	}
	var sb strings.Builder
	for _, post := range posts {
		sb.WriteString(renderPost(post))
		sb.WriteString("---\r\n")
	}
	_ = p.Write(sb.String())
}

func (h *TerminalHandler) startBoardPost(p *Participant, args string) {
	board, rest := splitTerminalCmd(args)
	if board == "" {
		_ = p.Write(`bbpost: usage "bbpost <board> \"<subject>\""` + "\r\n")
		return
	}
	subject, ok := parseQuotedSubject(rest)
	if !ok || subject == "" {
		_ = p.Write(`bbpost: subject must be quoted, e.g. bbpost general "Hello"` + "\r\n")
		return
	}
	h.beginCompose(p, &composeState{
		kind: "bb-post", args: []string{board}, subject: subject,
	})
}

func (h *TerminalHandler) startBoardReply(p *Participant, ctx context.Context, acc *auth.Account, args string) {
	board, idStr := splitTerminalCmd(args)
	if board == "" || idStr == "" {
		_ = p.Write("bbreply: usage `bbreply <board> <id>`\r\n")
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = p.Write("bbreply: numeric id required.\r\n")
		return
	}
	parent, err := h.deps.Boards.ReadPost(ctx, board, id, acc)
	if err != nil {
		_ = p.Write(fmt.Sprintf("bbreply: %v\r\n", err))
		return
	}
	subj := parent.Subject
	if !strings.HasPrefix(strings.ToLower(subj), "re:") {
		subj = "Re: " + subj
	}
	h.beginCompose(p, &composeState{
		kind: "bb-reply", args: []string{board, idStr}, subject: subj,
	})
}

func (h *TerminalHandler) catchUpBoard(p *Participant, ctx context.Context, acc *auth.Account, args string) {
	board := strings.TrimSpace(args)
	if board == "" {
		_ = p.Write("bbcatchup: usage `bbcatchup <board>`\r\n")
		return
	}
	if err := h.deps.Boards.CatchUp(ctx, board, acc); err != nil {
		_ = p.Write(fmt.Sprintf("bbcatchup: %v\r\n", err))
		return
	}
	_ = p.Write("Marked all posts read.\r\n")
}

// -- upload/download ----------------------------------------------------

func (h *TerminalHandler) handleUpload(p *Participant, args string) {
	if h.deps == nil || h.deps.Files == nil || h.deps.UploadURL == nil || h.deps.AccountFor == nil {
		_ = p.Write("upload: not yet implemented (M6).\r\n")
		return
	}
	ctx := context.Background()
	acc, err := h.deps.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		_ = p.Write("upload: cannot resolve account.\r\n")
		return
	}
	slug, ok := parseQuotedSubject(args)
	if !ok || slug == "" {
		// also accept bare slug without quotes
		slug = strings.TrimSpace(args)
	}
	slug, _ = splitTerminalCmd(slug) // drop optional description for now
	if slug == "" {
		_ = p.Write(`upload: usage "upload \"<slug>\""` + "\r\n")
		return
	}
	tok, err := h.deps.Files.IssueUpload(ctx, acc.ID, slug, 5*time.Minute)
	if err != nil {
		_ = p.Write(fmt.Sprintf("upload: %v\r\n", err))
		return
	}
	url := h.deps.UploadURL(tok.Value)
	_ = p.Write(fmt.Sprintf("Upload URL (valid for 5 minutes):\r\n  %s\r\n\r\nExample: curl --upload-file <local> %s\r\n", url, url))
}

func (h *TerminalHandler) handleDownload(p *Participant, args string) {
	if h.deps == nil || h.deps.Files == nil || h.deps.DownloadURL == nil || h.deps.AccountFor == nil {
		_ = p.Write("download: not yet implemented (M6).\r\n")
		return
	}
	ctx := context.Background()
	acc, err := h.deps.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		_ = p.Write("download: cannot resolve account.\r\n")
		return
	}
	slug := strings.TrimSpace(args)
	if slug == "" {
		_ = p.Write("download: usage `download <slug>`\r\n")
		return
	}
	f, err := h.deps.Files.GetFile(ctx, slug)
	if err != nil {
		_ = p.Write(fmt.Sprintf("download: %v\r\n", err))
		return
	}
	tok, err := h.deps.Files.IssueDownload(ctx, acc.ID, f.ID, 5*time.Minute)
	if err != nil {
		_ = p.Write(fmt.Sprintf("download: %v\r\n", err))
		return
	}
	url := h.deps.DownloadURL(tok.Value)
	_ = p.Write(fmt.Sprintf("Download URL (valid for 5 minutes):\r\n  %s\r\n", url))
}

// -- paste-mode compose -------------------------------------------------

func (h *TerminalHandler) beginCompose(p *Participant, c *composeState) {
	h.compose = c
	_ = p.Write("Enter your message. End with a single line containing only `.` (or `/abort` to cancel).\r\n")
}

func (h *TerminalHandler) handleComposeLine(p *Participant, line string) {
	switch strings.TrimSpace(line) {
	case ".":
		c := h.compose
		h.compose = nil
		h.commitCompose(p, c)
	case "/abort":
		h.compose = nil
		_ = p.Write("Compose aborted.\r\n")
		h.writePrompt(p)
	default:
		h.compose.body = append(h.compose.body, line)
		// no prompt — paste mode is silent until "."
	}
}

func (h *TerminalHandler) commitCompose(p *Participant, c *composeState) {
	ctx := context.Background()
	body := strings.Join(c.body, "\n")
	switch c.kind {
	case "mail-send":
		acc, _ := h.deps.AccountFor(p.PlayerID)
		_, err := h.deps.Mail.Send(ctx, acc, c.args[0], c.subject, body, "")
		if err != nil {
			_ = p.Write(fmt.Sprintf("mail send: %v\r\n", err))
		} else {
			_ = p.Write("Message sent.\r\n")
		}
	case "mail-reply":
		acc, _ := h.deps.AccountFor(p.PlayerID)
		_, err := h.deps.Mail.Send(ctx, acc, c.args[0], c.subject, body, c.args[1])
		if err != nil {
			_ = p.Write(fmt.Sprintf("mail reply: %v\r\n", err))
		} else {
			_ = p.Write("Reply sent.\r\n")
		}
	case "bb-post":
		acc, _ := h.deps.AccountFor(p.PlayerID)
		_, err := h.deps.Boards.Post(ctx, c.args[0], acc, c.subject, body)
		if err != nil {
			_ = p.Write(fmt.Sprintf("bbpost: %v\r\n", err))
		} else {
			_ = p.Write("Posted.\r\n")
		}
	case "bb-reply":
		acc, _ := h.deps.AccountFor(p.PlayerID)
		parentID, _ := strconv.ParseInt(c.args[1], 10, 64)
		_, err := h.deps.Boards.Reply(ctx, c.args[0], acc, parentID, c.subject, body)
		if err != nil {
			_ = p.Write(fmt.Sprintf("bbreply: %v\r\n", err))
		} else {
			_ = p.Write("Reply posted.\r\n")
		}
	}
	h.writePrompt(p)
}

// -- helpers ------------------------------------------------------------

func (h *TerminalHandler) writePrompt(p *Participant) {
	if h.compose != nil {
		_ = p.Write(".> ")
		return
	}
	prompt := h.host.Prompt
	if prompt == "" {
		prompt = "terminal> "
	}
	_ = p.Write(prompt)
}

func (h *TerminalHandler) writeHelp(p *Participant) {
	var b strings.Builder
	b.WriteString("Terminal commands:\r\n")
	for _, c := range terminalCommands {
		b.WriteString("  ")
		b.WriteString(c)
		b.WriteString("\r\n")
	}
	b.WriteString("Meta:\r\n")
	b.WriteString("  disengage  — close the terminal\r\n")
	b.WriteString("  look, who, help — world commands (briefly leave the terminal view)\r\n")
	_ = p.Write(b.String())
}

func splitTerminalCmd(line string) (cmd, rest string) {
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

// parseQuotedSubject takes a string that may begin with `"...stuff..."`
// optionally followed by trailing text. It returns the unquoted subject
// and true on success. If the string doesn't start with a quote it
// returns the original string and false.
func parseQuotedSubject(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '"' {
		return s, false
	}
	end := strings.IndexByte(s[1:], '"')
	if end < 0 {
		return "", false
	}
	return s[1 : 1+end], true
}

func renderPost(post boards.Post) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] From: %s <%s>\r\nDate: %s\r\nSubject: %s\r\nMSGID: %s\r\n",
		post.ID, post.AuthorName, post.OriginAddr,
		post.PostedAt.Format(time.RFC1123Z), post.Subject, post.MSGID)
	if post.ReplyToMSGID.Valid {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", post.ReplyToMSGID.String)
	}
	b.WriteString("\r\n")
	b.WriteString(post.Body)
	if !strings.HasSuffix(post.Body, "\n") {
		b.WriteString("\r\n")
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func pluralReply(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// errAccountResolve is reserved for tests that exercise the AccountFor
// failure path; not used by production code.
var errAccountResolve = errors.New("terminal: account resolve failed")

// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/files"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// ---------------------------------------------------------------------------
// Top-level admin console.

type adminMain struct{}

// adminMainEntries is the fixed ordered list of subsection labels rendered
// in the admin console. Each row's index plus one is the selector. Keeping
// it as a value rather than a derived list keeps render and handle in sync
// without an enum/switch pair.
var adminMainEntries = []struct {
	label string
	state state
}{
	{"Users", adminUsersList{}},
	{"Mail", adminMail{}},
	{"Boards", adminBoardsEntry{}},
	{"Files", adminFilesAreas{}},
	{"Objects", adminObjects{}},
	{"Rooms", adminRooms{}},
	{"FTN networks", adminFTN{}},
	{"System", adminSystem{}},
}

func (adminMain) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	for i, e := range adminMainEntries {
		rows = append(rows, Row{
			Selector: fmt.Sprintf("%d)", i+1),
			Label:    e.label,
		})
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "B)", Label: "Back"},
		Row{Selector: "Q)", Label: "Quit"},
		Row{Blank: true},
	)
	f := Frame{Width: h.width0(), Title: "admin console", Rows: rows}
	return f.String() + "Select: "
}

func (adminMain) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, mainMenu{})
		return
	case "q", "quit":
		h.mu.Lock()
		fn := h.disengage
		h.mu.Unlock()
		if fn != nil {
			fn()
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil && n >= 1 && n <= len(adminMainEntries) {
		h.transition(p, adminMainEntries[n-1].state)
		return
	}
	h.redraw(p)
}

// ---------------------------------------------------------------------------
// Users subsection.

type adminUsersList struct{ page int }

func (s adminUsersList) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	accs, err := loadAccounts(h)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to list accounts: " + err.Error() + ")"})
	case len(accs) == 0:
		rows = append(rows, Row{Label: "(no accounts)"})
	default:
		start, end := pageWindow(s.page, len(accs))
		for i := start; i < end; i++ {
			a := accs[i]
			last := "never"
			if a.LastLoginAt != nil {
				last = a.LastLoginAt.Format("2006-01-02")
			}
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%-20s %-8s  last login: %s", a.Username, a.AccessLevel, last),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(accs))
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	f := Frame{Width: h.width0(), Title: "admin — Users", Rows: rows}
	return f.String() + "Select: "
}

func (s adminUsersList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminMain{})
		return
	case "n", "next":
		accs, _ := loadAccounts(h)
		if (s.page+1)*pageSize < len(accs) {
			h.transition(p, adminUsersList{page: s.page + 1})
		} else {
			h.redraw(p)
		}
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminUsersList{page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		accs, _ := loadAccounts(h)
		if n >= 1 && n <= len(accs) {
			h.transition(p, adminUserView{id: accs[n-1].ID})
			return
		}
	}
	h.redraw(p)
}

type adminUserView struct{ id int64 }

func (s adminUserView) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.Auth == nil {
		return frame(h, "admin — User", append(rows,
			Row{Label: "(auth service unavailable)"},
			Row{Blank: true},
			Row{Selector: "B)", Label: "Back"},
			Row{Blank: true},
		))
	}
	acc, err := d.Auth.GetByID(h.ctx(), s.id)
	if err != nil {
		return frame(h, "admin — User", append(rows,
			Row{Label: "(unable to load: " + err.Error() + ")"},
			Row{Blank: true},
			Row{Selector: "B)", Label: "Back"},
			Row{Blank: true},
		))
	}
	last := "never"
	if acc.LastLoginAt != nil {
		last = acc.LastLoginAt.Format("2006-01-02 15:04")
	}
	rows = append(rows,
		Row{Label: "Username:    " + acc.Username},
		Row{Label: "Access:      " + string(acc.AccessLevel)},
		Row{Label: "Created:     " + acc.CreatedAt.Format("2006-01-02")},
		Row{Label: "Last login:  " + last},
		Row{Label: "Encoding:    " + ptrOrDash(acc.TerminalEncoding)},
		Row{Label: "Width:       " + intPtrOrDash(acc.TerminalWidth)},
		Row{Label: "Height:      " + intPtrOrDash(acc.TerminalHeight)},
		Row{Blank: true},
	)
	for i, a := range userViewActions(acc.AccessLevel) {
		rows = append(rows, Row{
			Selector: fmt.Sprintf("%d)", i+1),
			Label:    a.label,
		})
	}
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — User", rows) + "Select: "
}

// userViewActionKind tags an admin-menu action on the user-view screen.
type userViewActionKind int

const (
	userActionSetTier userViewActionKind = iota
	userActionResetPassword
)

// userViewAction is one entry rendered on the user-view screen. Tier
// transitions carry the target level; the reset-password action ignores
// level and is dispatched purely by kind.
type userViewAction struct {
	label string
	kind  userViewActionKind
	level auth.AccessLevel
}

// userViewActions returns the ordered list of actions an admin may take
// on a target at the given current level. Tier transitions come first
// so their selector indices stay stable as features are added below.
// Numbered selectors are used (not single letters) to avoid colliding
// with pagination keys in the parent users-list state — N/P move
// between pages there; reflex-typing "p" should never escalate a
// target to admin one screen deeper.
func userViewActions(current auth.AccessLevel) []userViewAction {
	var tiers []userViewAction
	switch current {
	case auth.AccessPlayer:
		tiers = []userViewAction{
			{"Promote to builder", userActionSetTier, auth.AccessBuilder},
			{"Promote to admin", userActionSetTier, auth.AccessAdmin},
		}
	case auth.AccessBuilder:
		tiers = []userViewAction{
			{"Demote to player", userActionSetTier, auth.AccessPlayer},
			{"Promote to admin", userActionSetTier, auth.AccessAdmin},
		}
	case auth.AccessAdmin:
		tiers = []userViewAction{
			{"Demote to player", userActionSetTier, auth.AccessPlayer},
			{"Demote to builder", userActionSetTier, auth.AccessBuilder},
		}
	}
	return append(tiers, userViewAction{
		label: "Reset password",
		kind:  userActionResetPassword,
	})
}

func (s adminUserView) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	if low == "" {
		h.redraw(p)
		return
	}
	if low == "b" || low == "back" || low == ".." {
		h.transition(p, adminUsersList{})
		return
	}
	d := getDeps(h)
	if d == nil || d.Auth == nil {
		h.redraw(p)
		return
	}
	target, err := d.Auth.GetByID(h.ctx(), s.id)
	if err != nil {
		h.redraw(p)
		return
	}
	actions := userViewActions(target.AccessLevel)
	n, perr := strconv.Atoi(low)
	if perr != nil || n < 1 || n > len(actions) {
		h.redraw(p)
		return
	}
	action := actions[n-1]
	actor, _ := h.accountFor(p)
	if actor != nil && actor.ID == target.ID {
		h.redraw(p)
		switch action.kind {
		case userActionResetPassword:
			_ = p.Write("Cannot reset your own password. Ask another admin.\r\n")
		default:
			_ = p.Write("Cannot change your own access level. Ask another admin.\r\n")
		}
		return
	}
	switch action.kind {
	case userActionSetTier:
		if action.level == target.AccessLevel {
			h.redraw(p)
			return
		}
		if err := d.Auth.SetAccessLevel(h.ctx(), target.ID, action.level); err != nil {
			_ = p.Write(fmt.Sprintf("error: %v\r\n", err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "set_access_level", target.Username, "level", string(action.level))
		h.redraw(p)
	case userActionResetPassword:
		s.issueReset(h, p, target)
	}
}

// issueReset generates a fresh password-reset token for target, sends
// the audit mail (best-effort), and transitions to the reveal screen
// that shows the plaintext once. The plaintext is never echoed into
// the audit log or the mail body — only the admin's terminal sees it.
func (adminUserView) issueReset(h *Handler, p *engage.Participant, target *auth.Account) {
	d := getDeps(h)
	if d == nil || d.Auth == nil {
		h.redraw(p)
		return
	}
	token, err := d.Auth.IssueReset(h.ctx(), target.ID, auth.ResetTokenTTL)
	if err != nil {
		_ = p.Write(fmt.Sprintf("Reset failed: %v\r\n", err))
		return
	}
	actor, _ := h.accountFor(p)
	actorName := ""
	if actor != nil {
		actorName = actor.Username
	}
	adminAudit(h, p, "password_reset_issued", target.Username,
		"expires_at", time.Now().Add(auth.ResetTokenTTL).Unix())
	// Best-effort audit mail. A missing or failing mail service must
	// not block the issuance — the admin still sees the token on
	// screen and can relay it out of band.
	if d.Mail != nil {
		subject := fmt.Sprintf("Password reset by %s", coalesce(actorName, "an administrator"))
		body := fmt.Sprintf(
			"An administrator (%s) has issued a one-time password reset for your account.\r\n"+
				"The token is valid for %d hours; if you did not request this, contact an administrator immediately.\r\n",
			coalesce(actorName, "unknown"),
			int(auth.ResetTokenTTL/time.Hour),
		)
		if _, mailErr := d.Mail.SendFromSystem(h.ctx(), target.Username, subject, body); mailErr != nil && d.Logger != nil {
			d.Logger.Warn("password_reset_mail_failed",
				"actor", actorName, "target", target.Username, "err", mailErr)
		}
	}
	h.transition(p, adminUserResetIssued{id: target.ID, username: target.Username, token: token})
}

// adminUserResetIssued reveals a freshly issued reset token to the
// admin. The plaintext is held in memory only for the lifetime of this
// state; navigating back to the user view drops it.
type adminUserResetIssued struct {
	id       int64
	username string
	token    string
}

func (s adminUserResetIssued) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Password reset issued for " + s.username + "."},
		{Blank: true},
		{Label: "One-time token (valid " + strconv.Itoa(int(auth.ResetTokenTTL/time.Hour)) + " hours):"},
		{Label: "    " + s.token},
		{Blank: true},
		{Label: "Relay this to the user out of band. It will not be shown again,"},
		{Label: "and a notification mail has been sent (no token in the body)."},
		{Blank: true},
		{Selector: "B)", Label: "Back"},
		{Blank: true},
	}
	return frame(h, "admin — User — Reset", rows) + "Select: "
}

func (s adminUserResetIssued) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "", "b", "back", "..":
		h.transition(p, adminUserView{id: s.id})
		return
	}
	h.redraw(p)
}

// coalesce returns the first non-empty argument, or "" if all are empty.
// Local helper for assembling mail subjects/bodies when the actor name
// may legitimately be unset (test paths without an authenticated admin).
func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Mail subsection.

type adminMail struct{}

func (adminMail) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Selector: "1)", Label: "Broadcast a system mail"},
		{Selector: "2)", Label: "Purge a message from a user's inbox"},
		{Blank: true},
		{Selector: "B)", Label: "Back"},
		{Blank: true},
	}
	return frame(h, "admin — Mail", rows)
}

func (adminMail) handle(h *Handler, p *engage.Participant, line string) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		h.redraw(p)
	case "b", "back", "..":
		h.transition(p, adminMain{})
	case "1":
		h.transition(p, adminMailBroadcastSubject{})
	case "2":
		h.transition(p, adminMailPurgeUser{})
	default:
		h.redraw(p)
	}
}

type adminMailBroadcastSubject struct{}

func (adminMailBroadcastSubject) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Broadcast a message to every account."},
		{Label: "Type a subject, or /abort to cancel."},
		{Blank: true},
	}
	return frame(h, "admin — Mail — Broadcast (Subject)", rows) + "Subject: "
}

func (adminMailBroadcastSubject) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminMail{})
		return
	}
	h.transition(p, adminMailBroadcastBody{subject: trimmed})
}

type adminMailBroadcastBody struct {
	subject string
	body    []string
}

func (s adminMailBroadcastBody) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Subject: " + truncRune(s.subject, 50)},
		{Blank: true},
		{Label: "Enter your message. End with a single line containing only `.`"},
		{Label: "or /abort to cancel."},
		{Blank: true},
	}
	return frame(h, "admin — Mail — Broadcast (Body)", rows) + ".> "
}

func (s adminMailBroadcastBody) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case "/abort":
		h.transition(p, adminMail{})
		return
	case ".":
		d := getDeps(h)
		if d == nil || d.Mail == nil || d.Auth == nil {
			h.transition(p, adminMail{})
			return
		}
		body := strings.Join(s.body, "\n")
		accs, err := d.Auth.ListAccounts(h.ctx())
		if err != nil {
			_ = p.Write(fmt.Sprintf("broadcast: %v\r\n", err))
			h.transition(p, adminMail{})
			return
		}
		sent := 0
		for _, a := range accs {
			if _, err := d.Mail.SendFromSystem(h.ctx(), a.Username, s.subject, body); err == nil {
				sent++
			}
		}
		adminAudit(h, p, "broadcast_mail", "*", "recipients", sent, "subject", s.subject)
		_ = p.Write(fmt.Sprintf("Broadcast sent to %d account(s).\r\n", sent))
		h.transition(p, adminMail{})
		return
	}
	s.body = append(s.body, line)
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
	_ = p.Write(".> ")
}

type adminMailPurgeUser struct{}

func (adminMailPurgeUser) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Type the username whose inbox to purge from, or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Mail — Purge", rows) + "User: "
}

func (adminMailPurgeUser) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminMail{})
		return
	}
	d := getDeps(h)
	if d == nil || d.Auth == nil {
		h.transition(p, adminMail{})
		return
	}
	target, err := d.Auth.GetByUsername(h.ctx(), trimmed)
	if err != nil {
		_ = p.Write(fmt.Sprintf("purge: %v\r\n", err))
		h.redraw(p)
		return
	}
	h.transition(p, adminMailPurgeChoose{accID: target.ID, username: target.Username})
}

type adminMailPurgeChoose struct {
	accID    int64
	username string
}

func (s adminMailPurgeChoose) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}, {Label: "User: " + s.username}, {Blank: true}}
	if d == nil || d.Mail == nil {
		rows = append(rows, Row{Label: "(mail service unavailable)"})
	} else {
		box, err := d.Mail.Inbox(h.ctx(), s.accID)
		if err != nil {
			rows = append(rows, Row{Label: "(unable to read inbox: " + err.Error() + ")"})
		} else if len(box) == 0 {
			rows = append(rows, Row{Label: "(inbox is empty)"})
		} else {
			for i, m := range box {
				rows = append(rows, Row{
					Selector: fmt.Sprintf("%d)", i+1),
					Label:    fmt.Sprintf("%-12s  %s", truncRune(m.FromName, 12), truncRune(m.Subject, 32)),
				})
			}
		}
	}
	rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — Mail — Purge", rows) + "Select: "
}

func (s adminMailPurgeChoose) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	if low == "" {
		h.redraw(p)
		return
	}
	if low == "b" || low == "back" || low == ".." {
		h.transition(p, adminMail{})
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		d := getDeps(h)
		if d == nil || d.Mail == nil {
			h.redraw(p)
			return
		}
		box, err := d.Mail.Inbox(h.ctx(), s.accID)
		if err != nil || n < 1 || n > len(box) {
			h.redraw(p)
			return
		}
		msg := box[n-1]
		if err := d.Mail.Delete(h.ctx(), msg.ID, s.accID); err != nil {
			_ = p.Write(fmt.Sprintf("purge: %v\r\n", err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "purge_mail", s.username, "mail_id", msg.ID, "subject", msg.Subject)
		_ = p.Write(fmt.Sprintf("Purged message %d from %s's inbox.\r\n", msg.ID, s.username))
		h.redraw(p)
		return
	}
	h.redraw(p)
}

// ---------------------------------------------------------------------------
// Boards subsection.

// adminBoardsEntry routes the admin into the boards UI. If exactly one
// FTN network is configured, the network-prompt is skipped and the
// admin lands directly on that network's board list.
type adminBoardsEntry struct{}

func (adminBoardsEntry) render(h *Handler, p *engage.Participant) string {
	d := getDeps(h)
	if d == nil || d.DB == nil || d.Boards == nil {
		return frame(h, "admin — Boards", []Row{
			{Blank: true},
			{Label: "(boards service unavailable)"},
			{Blank: true},
			{Selector: "B)", Label: "Back"},
			{Blank: true},
		})
	}
	nets, err := loadNetworks(h)
	if err != nil || len(nets) == 0 {
		return frame(h, "admin — Boards", []Row{
			{Blank: true},
			{Label: "(no FTN networks configured)"},
			{Blank: true},
			{Selector: "B)", Label: "Back"},
			{Blank: true},
		})
	}
	if len(nets) == 1 {
		return adminBoardsList{network: nets[0].Slug}.render(h, p)
	}
	rows := []Row{{Blank: true}}
	for i, n := range nets {
		label := n.Slug
		if n.IsDefault {
			label += "  (default)"
		}
		rows = append(rows, Row{
			Selector: fmt.Sprintf("%d)", i+1),
			Label:    label,
		})
	}
	rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — Boards — pick a network", rows) + "Select: "
}

func (adminBoardsEntry) handle(h *Handler, p *engage.Participant, line string) {
	nets, err := loadNetworks(h)
	if err != nil {
		h.transition(p, adminMain{})
		return
	}
	low := strings.ToLower(strings.TrimSpace(line))
	if low == "b" || low == "back" || low == ".." {
		h.transition(p, adminMain{})
		return
	}
	// Single-network shortcut: any input delegates into the list state.
	if len(nets) == 1 {
		adminBoardsList{network: nets[0].Slug}.handle(h, p, line)
		return
	}
	if low == "" {
		h.redraw(p)
		return
	}
	if n, err := strconv.Atoi(low); err == nil && n >= 1 && n <= len(nets) {
		h.transition(p, adminBoardsList{network: nets[n-1].Slug})
		return
	}
	h.redraw(p)
}

type adminBoardsList struct {
	network string
	page    int
}

func (s adminBoardsList) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	bs, err := loadBoardsInNetwork(h, s.network)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to list boards: " + err.Error() + ")"})
	case len(bs) == 0:
		rows = append(rows, Row{Label: "(no boards in network)"})
	default:
		start, end := pageWindow(s.page, len(bs))
		for i := start; i < end; i++ {
			b := bs[i]
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%-20s %s", b.Slug, truncRune(b.Name, 30)),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(bs))
	rows = append(rows,
		Row{Selector: "C)", Label: "Create board"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Boards — "+s.network, rows) + "Select: "
}

func (s adminBoardsList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		// Multi-network: the admin reached this list via the network
		// picker, so Back returns there. Single-network: the picker is
		// skipped on entry, so returning to it would be useless — go
		// straight back to the admin main menu instead.
		nets, _ := loadNetworks(h)
		if len(nets) > 1 {
			h.transition(p, adminBoardsEntry{})
		} else {
			h.transition(p, adminMain{})
		}
		return
	case "c", "create":
		h.transition(p, adminBoardsCreateSlug{network: s.network})
		return
	case "n", "next":
		bs, _ := loadBoardsInNetwork(h, s.network)
		if (s.page+1)*pageSize < len(bs) {
			h.transition(p, adminBoardsList{network: s.network, page: s.page + 1})
		} else {
			h.redraw(p)
		}
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminBoardsList{network: s.network, page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		bs, _ := loadBoardsInNetwork(h, s.network)
		if n >= 1 && n <= len(bs) {
			h.transition(p, adminBoardView{network: s.network, slug: bs[n-1].Slug})
			return
		}
	}
	h.redraw(p)
}

type adminBoardView struct{ network, slug string }

func (s adminBoardView) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.Boards == nil {
		rows = append(rows, Row{Label: "(boards service unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Boards", rows)
	}
	b, err := d.Boards.GetBoard(h.ctx(), s.slug)
	if err != nil {
		rows = append(rows, Row{Label: "(unable to load: " + err.Error() + ")"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Boards", rows)
	}
	areaTag := "(none)"
	if b.AreaTag.Valid {
		areaTag = b.AreaTag.String
	}
	rows = append(rows,
		Row{Label: "Slug:     " + b.Slug},
		Row{Label: "Name:     " + b.Name},
		Row{Label: "Network:  " + s.network},
		Row{Label: "Area tag: " + areaTag},
		Row{Label: fmt.Sprintf("Read >=   %d   Post >= %d   Admin >= %d",
			b.ReadMinLevel, b.PostMinLevel, b.AdminMinLevel)},
		Row{Blank: true},
		Row{Selector: "R)", Label: "Set read level"},
		Row{Selector: "P)", Label: "Set post level"},
		Row{Selector: "A)", Label: "Set admin level"},
		Row{Selector: "T)", Label: "Set area tag"},
		Row{Selector: "D)", Label: "Delete (must be empty)"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Boards — "+s.slug, rows) + "Select: "
}

func (s adminBoardView) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminBoardsList{network: s.network})
		return
	case "r":
		h.transition(p, adminBoardsACL{network: s.network, slug: s.slug, which: "read"})
		return
	case "p":
		h.transition(p, adminBoardsACL{network: s.network, slug: s.slug, which: "post"})
		return
	case "a":
		h.transition(p, adminBoardsACL{network: s.network, slug: s.slug, which: "admin"})
		return
	case "t":
		h.transition(p, adminBoardsAreaTag{network: s.network, slug: s.slug})
		return
	case "d":
		d := getDeps(h)
		if d == nil || d.Boards == nil {
			h.redraw(p)
			return
		}
		if err := d.Boards.DeleteBoard(h.ctx(), s.slug); err != nil {
			_ = p.Write(fmt.Sprintf("delete: %v\r\n", err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "delete_board", s.slug)
		_ = p.Write("Board deleted.\r\n")
		h.transition(p, adminBoardsList{network: s.network})
		return
	}
	h.redraw(p)
}

type adminBoardsCreateSlug struct{ network string }

func (s adminBoardsCreateSlug) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Network: " + s.network},
		{Label: "Type a slug for the new board, or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Boards — Create", rows) + "Slug: "
}

func (s adminBoardsCreateSlug) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminBoardsList{network: s.network})
		return
	}
	h.transition(p, adminBoardsCreateName{network: s.network, slug: trimmed})
}

type adminBoardsCreateName struct{ network, slug string }

func (s adminBoardsCreateName) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Network: " + s.network},
		{Label: "Slug:    " + s.slug},
		{Label: "Type a display name, or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Boards — Create", rows) + "Name: "
}

func (s adminBoardsCreateName) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminBoardsList{network: s.network})
		return
	}
	h.transition(p, adminBoardsCreateDesc{network: s.network, slug: s.slug, name: trimmed})
}

type adminBoardsCreateDesc struct {
	network, slug, name string
	body                []string
}

func (s adminBoardsCreateDesc) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Slug: " + s.slug},
		{Label: "Name: " + s.name},
		{Blank: true},
		{Label: "Enter a description. End with a single line containing only `.`"},
		{Label: "or /abort to cancel."},
		{Blank: true},
	}
	return frame(h, "admin — Boards — Create (Description)", rows) + ".> "
}

func (s adminBoardsCreateDesc) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case "/abort":
		h.transition(p, adminBoardsList{network: s.network})
		return
	case ".":
		d := getDeps(h)
		if d == nil || d.Boards == nil {
			h.transition(p, adminBoardsList{network: s.network})
			return
		}
		desc := strings.Join(s.body, "\n")
		if _, err := d.Boards.CreateBoard(h.ctx(), boards.CreateBoardSpec{
			Slug: s.slug, Name: s.name, Description: desc,
			NetworkSlug: s.network,
		}); err != nil {
			_ = p.Write(fmt.Sprintf("create board: %v\r\n", err))
			h.transition(p, adminBoardsList{network: s.network})
			return
		}
		adminAudit(h, p, "create_board", s.slug, "network", s.network)
		_ = p.Write("Board created.\r\n")
		h.transition(p, adminBoardsList{network: s.network})
		return
	}
	s.body = append(s.body, line)
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
	_ = p.Write(".> ")
}

type adminBoardsACL struct {
	network, slug, which string
}

func (s adminBoardsACL) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Board: " + s.slug},
		{Label: fmt.Sprintf("Enter the minimum access level for %s (0=guest, 100=player, 200=builder, 300=admin)", s.which)},
		{Label: "or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Boards — Set "+s.which+" level", rows) + "Level: "
}

func (s adminBoardsACL) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminBoardView{network: s.network, slug: s.slug})
		return
	}
	level, err := strconv.Atoi(trimmed)
	if err != nil {
		_ = p.Write("Enter a number.\r\n")
		h.redraw(p)
		return
	}
	d := getDeps(h)
	if d == nil || d.Boards == nil {
		h.transition(p, adminBoardView{network: s.network, slug: s.slug})
		return
	}
	var setErr error
	switch s.which {
	case "read":
		setErr = d.Boards.SetReadMinLevel(h.ctx(), s.slug, level)
	case "post":
		setErr = d.Boards.SetPostMinLevel(h.ctx(), s.slug, level)
	case "admin":
		setErr = d.Boards.SetAdminMinLevel(h.ctx(), s.slug, level)
	}
	if setErr != nil {
		_ = p.Write(fmt.Sprintf("set %s level: %v\r\n", s.which, setErr))
		h.redraw(p)
		return
	}
	adminAudit(h, p, "set_board_"+s.which+"_level", s.slug, "level", level)
	h.transition(p, adminBoardView{network: s.network, slug: s.slug})
}

type adminBoardsAreaTag struct{ network, slug string }

func (s adminBoardsAreaTag) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Board: " + s.slug},
		{Label: "Enter the new area tag (blank line to clear), or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Boards — Set area tag", rows) + "Area tag: "
}

func (s adminBoardsAreaTag) handle(h *Handler, p *engage.Participant, line string) {
	if strings.TrimSpace(line) == "/abort" {
		h.transition(p, adminBoardView{network: s.network, slug: s.slug})
		return
	}
	d := getDeps(h)
	if d == nil || d.Boards == nil {
		h.transition(p, adminBoardView{network: s.network, slug: s.slug})
		return
	}
	// strings.TrimSpace empty → clear.
	newTag := strings.TrimSpace(line)
	if err := d.Boards.SetAreaTag(h.ctx(), s.slug, newTag); err != nil {
		_ = p.Write(fmt.Sprintf("set area tag: %v\r\n", err))
		h.redraw(p)
		return
	}
	adminAudit(h, p, "set_board_area_tag", s.slug, "area_tag", newTag)
	h.transition(p, adminBoardView{network: s.network, slug: s.slug})
}

// ---------------------------------------------------------------------------
// Files subsection (areas + files).

type adminFilesAreas struct{ page int }

func (s adminFilesAreas) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	areas, err := loadAreas(h)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to list areas: " + err.Error() + ")"})
	case len(areas) == 0:
		rows = append(rows, Row{Label: "(no areas)"})
	default:
		start, end := pageWindow(s.page, len(areas))
		for i := start; i < end; i++ {
			a := areas[i]
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%-16s %s", a.Slug, truncRune(a.Description, 40)),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(areas))
	rows = append(rows,
		Row{Selector: "C)", Label: "Create area"},
		Row{Selector: "J)", Label: "Run cleanup (janitor)"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Files — Areas", rows) + "Select: "
}

func (s adminFilesAreas) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminMain{})
		return
	case "c", "create":
		h.transition(p, adminFilesAreaCreateSlug{})
		return
	case "j", "janitor", "cleanup":
		runFilesJanitor(h, p)
		h.redraw(p)
		return
	case "n", "next":
		areas, _ := loadAreas(h)
		if (s.page+1)*pageSize < len(areas) {
			h.transition(p, adminFilesAreas{page: s.page + 1})
		} else {
			h.redraw(p)
		}
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminFilesAreas{page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		areas, _ := loadAreas(h)
		if n >= 1 && n <= len(areas) {
			h.transition(p, adminFilesAreaView{slug: areas[n-1].Slug})
			return
		}
	}
	h.redraw(p)
}

type adminFilesAreaView struct{ slug string }

func (s adminFilesAreaView) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.Files == nil {
		rows = append(rows, Row{Label: "(files service unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Files", rows)
	}
	a, err := d.Files.GetArea(h.ctx(), s.slug)
	if err != nil {
		rows = append(rows, Row{Label: "(area not found)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Files", rows)
	}
	rows = append(rows,
		Row{Label: "Slug:        " + a.Slug},
		Row{Label: "Description: " + a.Description},
		Row{Blank: true},
		Row{Selector: "L)", Label: "List files"},
		Row{Selector: "D)", Label: "Delete area (must be empty)"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Files — "+a.Slug, rows) + "Select: "
}

func (s adminFilesAreaView) handle(h *Handler, p *engage.Participant, line string) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		h.redraw(p)
	case "b", "back", "..":
		h.transition(p, adminFilesAreas{})
	case "l", "list":
		h.transition(p, adminFilesList{area: s.slug})
	case "d", "delete":
		d := getDeps(h)
		if d == nil || d.Files == nil {
			h.redraw(p)
			return
		}
		if err := d.Files.DeleteArea(h.ctx(), s.slug); err != nil {
			_ = p.Write(fmt.Sprintf("delete area: %v\r\n", err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "delete_area", s.slug)
		_ = p.Write("Area deleted.\r\n")
		h.transition(p, adminFilesAreas{})
	default:
		h.redraw(p)
	}
}

type adminFilesAreaCreateSlug struct{}

func (adminFilesAreaCreateSlug) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Type a slug for the new area, or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Files — Create area", rows) + "Slug: "
}

func (adminFilesAreaCreateSlug) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminFilesAreas{})
		return
	}
	h.transition(p, adminFilesAreaCreateDesc{slug: trimmed})
}

type adminFilesAreaCreateDesc struct{ slug string }

func (s adminFilesAreaCreateDesc) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Slug: " + s.slug},
		{Label: "Type a description, or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Files — Create area", rows) + "Description: "
}

func (s adminFilesAreaCreateDesc) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "/abort" {
		h.transition(p, adminFilesAreas{})
		return
	}
	d := getDeps(h)
	if d == nil || d.Files == nil {
		h.transition(p, adminFilesAreas{})
		return
	}
	if err := d.Files.CreateArea(h.ctx(), files.Area{
		Slug: s.slug, Name: s.slug, Description: trimmed,
	}); err != nil {
		_ = p.Write(fmt.Sprintf("create area: %v\r\n", err))
		h.redraw(p)
		return
	}
	adminAudit(h, p, "create_area", s.slug)
	_ = p.Write("Area created.\r\n")
	h.transition(p, adminFilesAreas{})
}

type adminFilesList struct {
	area string
	page int
}

func (s adminFilesList) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}, {Label: "Area: " + s.area}, {Blank: true}}
	d := getDeps(h)
	var list []files.File
	if d != nil && d.Files != nil {
		var err error
		list, err = d.Files.ListFiles(h.ctx(), files.ListFilter{Area: s.area})
		if err != nil {
			rows = append(rows, Row{Label: "(unable to list: " + err.Error() + ")"})
		}
	}
	if len(list) == 0 {
		rows = append(rows, Row{Label: "(no files)"})
	} else {
		start, end := pageWindow(s.page, len(list))
		for i := start; i < end; i++ {
			f := list[i]
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%-20s %d bytes", truncRune(f.Slug, 20), f.Size),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(list))
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — Files — "+s.area, rows) + "Select: "
}

func (s adminFilesList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminFilesAreaView{slug: s.area})
		return
	case "n", "next":
		d := getDeps(h)
		if d != nil && d.Files != nil {
			list, _ := d.Files.ListFiles(h.ctx(), files.ListFilter{Area: s.area})
			if (s.page+1)*pageSize < len(list) {
				h.transition(p, adminFilesList{area: s.area, page: s.page + 1})
				return
			}
		}
		h.redraw(p)
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminFilesList{area: s.area, page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		d := getDeps(h)
		if d == nil || d.Files == nil {
			h.redraw(p)
			return
		}
		list, _ := d.Files.ListFiles(h.ctx(), files.ListFilter{Area: s.area})
		if n >= 1 && n <= len(list) {
			h.transition(p, adminFileView{area: s.area, id: list[n-1].ID})
			return
		}
	}
	h.redraw(p)
}

type adminFileView struct {
	area string
	id   int64
}

func (s adminFileView) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.Files == nil {
		rows = append(rows, Row{Label: "(files service unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Files", rows)
	}
	list, err := d.Files.ListFiles(h.ctx(), files.ListFilter{Area: s.area})
	if err != nil {
		rows = append(rows, Row{Label: "(error: " + err.Error() + ")"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Files", rows)
	}
	var (
		f     files.File
		found bool
	)
	for i := range list {
		if list[i].ID == s.id {
			f = list[i]
			found = true
			break
		}
	}
	if !found {
		// The file went away between the list view and the view-detail
		// transition — most likely another admin deleted it concurrently.
		// Surface the gap explicitly rather than rendering an empty row
		// that hides the cause.
		rows = append(rows,
			Row{Label: fmt.Sprintf("(file id %d is no longer in area %q)", s.id, s.area)},
			Row{Blank: true},
			Row{Selector: "B)", Label: "Back"},
			Row{Blank: true},
		)
		return frame(h, "admin — Files", rows)
	}
	rows = append(rows,
		Row{Label: "Slug:        " + f.Slug},
		Row{Label: "Size:        " + strconv.FormatInt(f.Size, 10)},
		Row{Label: "SHA-256:     " + f.Hash},
		Row{Label: "Uploaded by: " + strconv.FormatInt(f.OwnerID, 10)},
		Row{Blank: true},
		Row{Selector: "D)", Label: "Delete"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Files — "+f.Slug, rows) + "Select: "
}

func (s adminFileView) handle(h *Handler, p *engage.Participant, line string) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "b", "back", "..":
		h.transition(p, adminFilesList{area: s.area})
	case "d", "delete":
		d := getDeps(h)
		if d == nil || d.Files == nil {
			h.redraw(p)
			return
		}
		if err := d.Files.DeleteFile(h.ctx(), s.id); err != nil {
			_ = p.Write(fmt.Sprintf("delete: %v\r\n", err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "delete_file", strconv.FormatInt(s.id, 10), "area", s.area)
		_ = p.Write("File deleted.\r\n")
		h.transition(p, adminFilesList{area: s.area})
	default:
		h.redraw(p)
	}
}

// ---------------------------------------------------------------------------
// Objects subsection.

type adminObjects struct{ page int }

func (s adminObjects) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	d := getDeps(h)
	if d == nil || d.World == nil {
		rows = append(rows, Row{Label: "(world unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Objects", rows)
	}
	objs := d.World.ListObjects()
	if len(objs) == 0 {
		rows = append(rows, Row{Label: "(no objects)"})
	} else {
		start, end := pageWindow(s.page, len(objs))
		for i := start; i < end; i++ {
			o := objs[i]
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%-20s %-8s %s", truncRune(o.Slug, 20), o.Kind, truncRune(o.Name, 24)),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(objs))
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — Objects", rows) + "Select: "
}

func (s adminObjects) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminMain{})
		return
	case "n", "next":
		d := getDeps(h)
		if d != nil && d.World != nil {
			if (s.page+1)*pageSize < len(d.World.ListObjects()) {
				h.transition(p, adminObjects{page: s.page + 1})
				return
			}
		}
		h.redraw(p)
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminObjects{page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		d := getDeps(h)
		if d == nil || d.World == nil {
			h.redraw(p)
			return
		}
		objs := d.World.ListObjects()
		if n >= 1 && n <= len(objs) {
			h.transition(p, adminObjectView{slug: objs[n-1].Slug})
			return
		}
	}
	h.redraw(p)
}

type adminObjectView struct{ slug string }

func (s adminObjectView) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.World == nil {
		rows = append(rows, Row{Label: "(world unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Object", rows)
	}
	o, err := d.World.ObjectBySlug(s.slug)
	if err != nil {
		rows = append(rows, Row{Label: "(unable to load: " + err.Error() + ")"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Object", rows)
	}
	rows = append(rows,
		Row{Label: "Slug:     " + o.Slug},
		Row{Label: "Name:     " + o.Name},
		Row{Label: "Kind:     " + string(o.Kind)},
		Row{Label: "Short:    " + o.ShortDesc},
		Row{Blank: true},
		Row{Selector: "D)", Label: "Delete"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Object — "+o.Slug, rows) + "Select: "
}

func (s adminObjectView) handle(h *Handler, p *engage.Participant, line string) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "b", "back", "..":
		h.transition(p, adminObjects{})
	case "d", "delete":
		d := getDeps(h)
		if d == nil || d.World == nil {
			h.redraw(p)
			return
		}
		o, err := d.World.ObjectBySlug(s.slug)
		if err != nil {
			_ = p.Write(fmt.Sprintf("delete: %v\r\n", err))
			h.redraw(p)
			return
		}
		if err := d.World.DeleteObject(h.ctx(), o.ID); err != nil {
			_ = p.Write(fmt.Sprintf("delete: %v\r\n", err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "delete_object", s.slug)
		_ = p.Write("Object deleted.\r\n")
		h.transition(p, adminObjects{})
	default:
		h.redraw(p)
	}
}

// ---------------------------------------------------------------------------
// Rooms subsection.

type adminRooms struct{ page int }

func (s adminRooms) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	d := getDeps(h)
	if d == nil || d.World == nil {
		rows = append(rows, Row{Label: "(world unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Rooms", rows)
	}
	rooms := d.World.ListRooms()
	if len(rooms) == 0 {
		rows = append(rows, Row{Label: "(no rooms)"})
	} else {
		start, end := pageWindow(s.page, len(rooms))
		for i := start; i < end; i++ {
			r := rooms[i]
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%-20s %s", truncRune(r.Slug, 20), truncRune(r.Name, 30)),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(rooms))
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — Rooms", rows) + "Select: "
}

func (s adminRooms) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminMain{})
		return
	case "n", "next":
		d := getDeps(h)
		if d != nil && d.World != nil {
			if (s.page+1)*pageSize < len(d.World.ListRooms()) {
				h.transition(p, adminRooms{page: s.page + 1})
				return
			}
		}
		h.redraw(p)
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminRooms{page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		d := getDeps(h)
		if d == nil || d.World == nil {
			h.redraw(p)
			return
		}
		rooms := d.World.ListRooms()
		if n >= 1 && n <= len(rooms) {
			h.transition(p, adminRoomView{slug: rooms[n-1].Slug})
			return
		}
	}
	h.redraw(p)
}

type adminRoomView struct{ slug string }

func (s adminRoomView) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.World == nil {
		rows = append(rows, Row{Label: "(world unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Room", rows)
	}
	r, err := d.World.RoomBySlug(s.slug)
	if err != nil {
		rows = append(rows, Row{Label: "(unable to load: " + err.Error() + ")"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — Room", rows)
	}
	exitList := "(none)"
	if len(r.Exits) > 0 {
		var dirs []string
		for dir := range r.Exits {
			dirs = append(dirs, dir)
		}
		exitList = strings.Join(dirs, ", ")
	}
	rows = append(rows,
		Row{Label: "Slug:    " + r.Slug},
		Row{Label: "Name:    " + r.Name},
		Row{Label: "Exits:   " + exitList},
		Row{Blank: true},
	)
	for _, ln := range splitBodyLines(r.Description, h.width0()-6) {
		rows = append(rows, Row{Label: ln})
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "E)", Label: "Edit description"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Room — "+r.Slug, rows) + "Select: "
}

func (s adminRoomView) handle(h *Handler, p *engage.Participant, line string) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "b", "back", "..":
		h.transition(p, adminRooms{})
	case "e", "edit":
		h.transition(p, adminRoomEditDesc{slug: s.slug})
	default:
		h.redraw(p)
	}
}

type adminRoomEditDesc struct {
	slug string
	body []string
}

func (s adminRoomEditDesc) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Room: " + s.slug},
		{Blank: true},
		{Label: "Enter the new description. End with `.` on its own line, or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — Room — Edit description", rows) + ".> "
}

func (s adminRoomEditDesc) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case "/abort":
		h.transition(p, adminRoomView{slug: s.slug})
		return
	case ".":
		d := getDeps(h)
		if d == nil || d.World == nil {
			h.transition(p, adminRoomView{slug: s.slug})
			return
		}
		r, err := d.World.RoomBySlug(s.slug)
		if err != nil {
			_ = p.Write(fmt.Sprintf("edit description: %v\r\n", err))
			h.transition(p, adminRoomView{slug: s.slug})
			return
		}
		desc := strings.Join(s.body, "\n")
		if err := d.World.SetRoomDescription(h.ctx(), r.ID, desc); err != nil {
			_ = p.Write(fmt.Sprintf("edit description: %v\r\n", err))
			h.transition(p, adminRoomView{slug: s.slug})
			return
		}
		adminAudit(h, p, "set_room_description", s.slug)
		_ = p.Write("Description updated.\r\n")
		h.transition(p, adminRoomView{slug: s.slug})
		return
	}
	s.body = append(s.body, line)
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
	_ = p.Write(".> ")
}

// ---------------------------------------------------------------------------
// FTN networks subsection.

type adminFTN struct{}

func (adminFTN) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	nets, err := loadNetworks(h)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to list: " + err.Error() + ")"})
	case len(nets) == 0:
		rows = append(rows, Row{Label: "(no networks)"})
	default:
		for i, n := range nets {
			label := fmt.Sprintf("%-12s %s", n.Slug, n.Name)
			if n.IsDefault {
				label += "  (default)"
			}
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    label,
			})
		}
	}
	rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — FTN networks", rows) + "Select: "
}

func (adminFTN) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	if low == "" {
		h.redraw(p)
		return
	}
	if low == "b" || low == "back" || low == ".." {
		h.transition(p, adminMain{})
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		nets, _ := loadNetworks(h)
		if n >= 1 && n <= len(nets) {
			h.transition(p, adminFTNView{slug: nets[n-1].Slug})
			return
		}
	}
	h.redraw(p)
}

type adminFTNView struct{ slug string }

func (s adminFTNView) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.DB == nil {
		rows = append(rows, Row{Label: "(db unavailable)"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — FTN", rows)
	}
	net, err := ftnnetworks.Get(h.ctx(), d.DB, s.slug)
	if err != nil {
		rows = append(rows, Row{Label: "(unable to load: " + err.Error() + ")"})
		rows = append(rows, Row{Blank: true}, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
		return frame(h, "admin — FTN", rows)
	}
	rows = append(rows,
		Row{Label: "Slug:    " + net.Slug},
		Row{Label: "Name:    " + net.Name},
		Row{Label: "Domain:  " + net.Domain},
		Row{Label: "Address: " + net.OurAddr},
		Row{Label: fmt.Sprintf("Default: %t", net.IsDefault)},
		Row{Blank: true},
	)
	if !net.IsDefault {
		rows = append(rows, Row{Selector: "S)", Label: "Set as default"})
	}
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — FTN — "+net.Slug, rows) + "Select: "
}

func (s adminFTNView) handle(h *Handler, p *engage.Participant, line string) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "b", "back", "..":
		h.transition(p, adminFTN{})
	case "s", "set":
		d := getDeps(h)
		if d == nil || d.DB == nil {
			h.redraw(p)
			return
		}
		if err := ftnnetworks.SetDefault(h.ctx(), d.DB, s.slug); err != nil {
			_ = p.Write(fmt.Sprintf("set default: %v\r\n", err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "set_default_network", s.slug)
		_ = p.Write("Default network set.\r\n")
		h.redraw(p)
	default:
		h.redraw(p)
	}
}

// ---------------------------------------------------------------------------
// System subsection.

type adminSystem struct{}

func (adminSystem) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	motd := "(none)"
	if d != nil && d.GetMOTD != nil {
		if m := d.GetMOTD(); m != "" {
			motd = m
		}
	}
	uptime := "unknown"
	if d != nil && !d.StartedAt.IsZero() {
		uptime = formatUptime(d.StartedAt)
	}
	stats := loadSystemStats(h)
	rows = append(rows,
		Row{Label: "Uptime:    " + uptime},
		Row{Label: fmt.Sprintf("Accounts:  %d", stats.accounts)},
		Row{Label: fmt.Sprintf("Sessions:  %d", stats.sessions)},
		Row{Label: fmt.Sprintf("Mail:      %d messages", stats.mail)},
		Row{Label: fmt.Sprintf("Boards:    %d", stats.boards)},
		Row{Label: fmt.Sprintf("Files:     %d", stats.files)},
		Row{Blank: true},
		Row{Label: "MOTD: " + truncRune(motd, h.width0()-10)},
		Row{Blank: true},
		Row{Selector: "E)", Label: "Edit MOTD"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — System", rows) + "Select: "
}

func (adminSystem) handle(h *Handler, p *engage.Participant, line string) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "b", "back", "..":
		h.transition(p, adminMain{})
	case "e", "edit":
		h.transition(p, adminSystemSetMOTD{})
	default:
		h.redraw(p)
	}
}

type adminSystemSetMOTD struct{ body []string }

func (s adminSystemSetMOTD) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Enter the new MOTD. End with `.` on its own line, or /abort."},
		{Blank: true},
	}
	return frame(h, "admin — System — Edit MOTD", rows) + ".> "
}

func (s adminSystemSetMOTD) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	switch trimmed {
	case "/abort":
		h.transition(p, adminSystem{})
		return
	case ".":
		d := getDeps(h)
		if d == nil || d.SetMOTD == nil {
			h.transition(p, adminSystem{})
			return
		}
		msg := strings.Join(s.body, "\n")
		if err := d.SetMOTD(h.ctx(), msg); err != nil {
			_ = p.Write(fmt.Sprintf("set MOTD: %v\r\n", err))
			h.transition(p, adminSystem{})
			return
		}
		adminAudit(h, p, "set_motd", "system")
		_ = p.Write("MOTD updated.\r\n")
		h.transition(p, adminSystem{})
		return
	}
	s.body = append(s.body, line)
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
	_ = p.Write(".> ")
}

// ---------------------------------------------------------------------------
// shared helpers

// getDeps returns the handler's deps under the lock.
func getDeps(h *Handler) *engage.TerminalDeps {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deps
}

// frame is a tiny render helper that wraps the supplied rows in a Frame
// of the handler's current width and appends a "Select: " prompt.
func frame(h *Handler, title string, rows []Row) string {
	f := Frame{Width: h.width0(), Title: title, Rows: rows}
	return f.String() + "Select: "
}

// pageWindow returns the [start, end) bounds for the requested page,
// clamped to the slice length.
func pageWindow(page, total int) (int, int) {
	start := page * pageSize
	end := start + pageSize
	if start > total {
		start = 0
		end = 0
	}
	if end > total {
		end = total
	}
	return start, end
}

// appendPageControls adds N) and P) rows when meaningful for the page.
func appendPageControls(rows []Row, page, total int) []Row {
	if page > 0 {
		rows = append(rows, Row{Selector: "P)", Label: "Previous page"})
	}
	if (page+1)*pageSize < total {
		rows = append(rows, Row{Selector: "N)", Label: "Next page"})
	}
	return append(rows, Row{Blank: true})
}

// adminAudit emits one structured slog line for an admin mutation. Skipped
// silently when the handler has no logger configured (test paths).
func adminAudit(h *Handler, p *engage.Participant, action, target string, args ...any) {
	d := getDeps(h)
	if d == nil || d.Logger == nil {
		return
	}
	actor := ""
	if acc, _ := h.accountFor(p); acc != nil {
		actor = acc.Username
	}
	fields := []any{"actor", actor, "action", action, "target", target}
	fields = append(fields, args...)
	d.Logger.Info("admin_audit", fields...)
}

func loadAccounts(h *Handler) ([]auth.Account, error) {
	d := getDeps(h)
	if d == nil || d.Auth == nil {
		return nil, nil
	}
	return d.Auth.ListAccounts(h.ctx())
}

func loadNetworks(h *Handler) ([]ftnnetworks.Network, error) {
	d := getDeps(h)
	if d == nil || d.DB == nil {
		return nil, nil
	}
	return ftnnetworks.List(h.ctx(), d.DB)
}

func loadBoardsInNetwork(h *Handler, networkSlug string) ([]boards.Board, error) {
	d := getDeps(h)
	if d == nil || d.Boards == nil {
		return nil, nil
	}
	return d.Boards.ListByNetwork(h.ctx(), networkSlug)
}

func loadAreas(h *Handler) ([]files.Area, error) {
	d := getDeps(h)
	if d == nil || d.Files == nil {
		return nil, nil
	}
	return d.Files.ListAreas(h.ctx())
}

func runFilesJanitor(h *Handler, p *engage.Participant) {
	d := getDeps(h)
	if d == nil || d.Files == nil {
		return
	}
	stats, err := d.Files.Janitor(h.ctx(), 0)
	if err != nil {
		_ = p.Write(fmt.Sprintf("janitor: %v\r\n", err))
		return
	}
	adminAudit(h, p, "files_janitor", "*",
		"tokens_reaped", stats.TokensReaped,
		"blobs_reaped", stats.BlobsReaped)
	_ = p.Write(fmt.Sprintf("Janitor: %d expired tokens, %d orphan blobs reaped.\r\n",
		stats.TokensReaped, stats.BlobsReaped))
}

// systemStats is the bundle of counts the System subsection renders. Zero
// values are fine for any service that's missing — the rendered row will
// just show 0.
type systemStats struct {
	accounts, sessions, mail, boards, files int
}

func loadSystemStats(h *Handler) systemStats {
	var s systemStats
	d := getDeps(h)
	if d == nil {
		return s
	}
	ctx := h.ctx()
	if d.Auth != nil {
		s.accounts, _ = d.Auth.Count(ctx)
	}
	if d.World != nil {
		s.sessions = len(d.World.WhoOnline())
	}
	if d.DB != nil {
		_ = d.DB.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM mail`).Scan(&s.mail)
		_ = d.DB.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM boards`).Scan(&s.boards)
		_ = d.DB.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM files`).Scan(&s.files)
	}
	return s
}

func formatUptime(started time.Time) string {
	d := time.Since(started)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours()/24), int(d.Hours())%24)
}

func ptrOrDash(p *string) string {
	if p == nil {
		return "—"
	}
	return *p
}

func intPtrOrDash(p *int) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p)
}

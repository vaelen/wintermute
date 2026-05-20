// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/security"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// adminUserRename prompts for the target's new username. /abort cancels.
type adminUserRename struct{ id int64 }

func (s adminUserRename) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	if d == nil || d.Auth == nil || d.RenameAccount == nil {
		return frame(h, "admin — User — Rename", []Row{
			{Blank: true},
			{Label: "(rename service unavailable)"},
			{Blank: true},
			{Selector: "B)", Label: "Back"},
			{Blank: true},
		})
	}
	target, err := d.Auth.GetByID(h.ctx(), s.id)
	if err != nil {
		return frame(h, "admin — User — Rename", []Row{
			{Blank: true},
			{Label: "(unable to load: " + err.Error() + ")"},
			{Blank: true},
			{Selector: "B)", Label: "Back"},
			{Blank: true},
		})
	}
	rows := []Row{
		{Blank: true},
		{Label: "Current username: " + target.Username},
		{Blank: true},
		{Label: "Type the new username, or /abort to cancel."},
		{Blank: true},
	}
	return frame(h, "admin — User — Rename", rows) + "New name: "
}

func (s adminUserRename) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" || strings.EqualFold(trimmed, "b") || trimmed == ".." {
		h.transition(p, adminUserView{id: s.id})
		return
	}
	d := getDeps(h)
	if d == nil || d.Auth == nil || d.RenameAccount == nil {
		h.redraw(p)
		return
	}
	target, err := d.Auth.GetByID(h.ctx(), s.id)
	if err != nil {
		_ = p.Write(fmt.Sprintf("rename: %v\r\n", err))
		h.transition(p, adminUserView{id: s.id})
		return
	}
	actor, _ := h.accountFor(p)
	var actorID int64
	if actor != nil {
		actorID = actor.ID
	}
	if err := d.RenameAccount(h.ctx(), target.Username, trimmed, actorID); err != nil {
		_ = p.Write(fmt.Sprintf("rename: %v\r\n", err))
		h.redraw(p)
		return
	}
	adminAudit(h, p, "rename_account", target.Username, "new", trimmed)
	_ = p.Write("Renamed " + target.Username + " → " + trimmed + ".\r\n")
	h.transition(p, adminUserView{id: s.id})
}

// adminUserHistory lists the username_history rows for one account,
// paginated, with no edit actions. Release is admin-wide and lives on
// the Security → Reserved names submenu instead.
type adminUserHistory struct {
	id   int64
	page int
}

func (s adminUserHistory) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	if d == nil || d.Security == nil {
		return frame(h, "admin — User — History", []Row{
			{Blank: true},
			{Label: "(security service unavailable)"},
			{Blank: true},
			{Selector: "B)", Label: "Back"},
			{Blank: true},
		})
	}
	rows := []Row{{Blank: true}}
	hist, err := d.Security.ListUsernameHistory(h.ctx(), s.id)
	if err != nil {
		rows = append(rows, Row{Label: "(unable to load: " + err.Error() + ")"})
	} else if len(hist) == 0 {
		rows = append(rows, Row{Label: "(no rename history for this account)"})
	} else {
		start, end := pageWindow(s.page, len(hist))
		for i := start; i < end; i++ {
			r := hist[i]
			when := time.Unix(r.RenamedAt, 0).UTC().Format("2006-01-02 15:04")
			rows = append(rows, Row{
				Label: fmt.Sprintf("%-20s → %-20s   %s", r.OldUsername, r.RenamedTo, when),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(hist))
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — User — History", rows) + "Select: "
}

func (s adminUserHistory) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
	case "b", "back", "..":
		h.transition(p, adminUserView{id: s.id})
	case "n", "next":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.redraw(p)
			return
		}
		hist, _ := d.Security.ListUsernameHistory(h.ctx(), s.id)
		if (s.page+1)*pageSize < len(hist) {
			h.transition(p, adminUserHistory{id: s.id, page: s.page + 1})
		} else {
			h.redraw(p)
		}
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminUserHistory{id: s.id, page: s.page - 1})
		} else {
			h.redraw(p)
		}
	default:
		h.redraw(p)
	}
}

// adminSecurity is the M6.6 Security top-level state.
type adminSecurity struct{}

var securityEntries = []struct {
	label string
	state state
}{
	{"Disallowed usernames", adminUsernamesList{}},
	{"Reserved names (history)", adminReservedNamesList{}},
	{"IP denials", adminIPDenialsList{}},
}

func (adminSecurity) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{{Blank: true}}
	for i, e := range securityEntries {
		rows = append(rows, Row{
			Selector: fmt.Sprintf("%d)", i+1),
			Label:    e.label,
		})
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Security", rows)
}

func (adminSecurity) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminMain{})
		return
	}
	if n, err := strconv.Atoi(low); err == nil && n >= 1 && n <= len(securityEntries) {
		h.transition(p, securityEntries[n-1].state)
		return
	}
	h.redraw(p)
}

// adminUsernamesList renders the disallow list with add / remove actions.
type adminUsernamesList struct{ page int }

func (s adminUsernamesList) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.Security == nil {
		return frame(h, "admin — Security — Disallowed usernames", append(rows,
			Row{Label: "(security service unavailable)"},
			Row{Blank: true},
			Row{Selector: "B)", Label: "Back"},
			Row{Blank: true},
		))
	}
	names, err := d.Security.ListDisallowed(h.ctx())
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to load: " + err.Error() + ")"})
	case len(names) == 0:
		rows = append(rows, Row{Label: "(disallow list is empty)"})
	default:
		start, end := pageWindow(s.page, len(names))
		for i := start; i < end; i++ {
			n := names[i]
			label := n.Username
			if n.Reason != "" {
				label += "  — " + n.Reason
			}
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    label,
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(names))
	rows = append(rows,
		Row{Selector: "A)", Label: "Add"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Security — Disallowed usernames", rows) + "Select: "
}

func (s adminUsernamesList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminSecurity{})
		return
	case "a", "add":
		h.transition(p, adminUsernameAdd{})
		return
	case "n", "next":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.redraw(p)
			return
		}
		names, _ := d.Security.ListDisallowed(h.ctx())
		if (s.page+1)*pageSize < len(names) {
			h.transition(p, adminUsernamesList{page: s.page + 1})
		} else {
			h.redraw(p)
		}
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminUsernamesList{page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.redraw(p)
			return
		}
		names, _ := d.Security.ListDisallowed(h.ctx())
		if n >= 1 && n <= len(names) {
			h.transition(p, adminUsernameRemove{name: names[n-1].Username})
			return
		}
	}
	h.redraw(p)
}

// adminUsernameAdd captures name + reason in a single line. The "reason"
// text is everything after the first space.
type adminUsernameAdd struct{}

func (adminUsernameAdd) render(h *Handler, _ *engage.Participant) string {
	return frame(h, "admin — Security — Disallow name", []Row{
		{Blank: true},
		{Label: "Type \"<name>\" or \"<name> <reason>\" to add."},
		{Label: "/abort cancels."},
		{Blank: true},
	}) + "Add: "
}

func (adminUsernameAdd) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminUsernamesList{})
		return
	}
	parts := strings.SplitN(trimmed, " ", 2)
	name := parts[0]
	reason := ""
	if len(parts) == 2 {
		reason = strings.TrimSpace(parts[1])
	}
	d := getDeps(h)
	if d == nil || d.Security == nil {
		h.transition(p, adminUsernamesList{})
		return
	}
	actor, _ := h.accountFor(p)
	var addedBy *int64
	if actor != nil {
		v := actor.ID
		addedBy = &v
	}
	if err := d.Security.DisallowUsername(h.ctx(), name, reason, addedBy); err != nil {
		_ = p.Write(securityErrLine(err))
		h.redraw(p)
		return
	}
	adminAudit(h, p, "username_deny_add", name, "reason", reason)
	_ = p.Write("Added \"" + name + "\" to the disallow list.\r\n")
	h.transition(p, adminUsernamesList{})
}

// adminUsernameRemove confirms removal of a single entry.
type adminUsernameRemove struct{ name string }

func (s adminUsernameRemove) render(h *Handler, _ *engage.Participant) string {
	return frame(h, "admin — Security — Remove", []Row{
		{Blank: true},
		{Label: "Remove \"" + s.name + "\" from the disallow list?"},
		{Blank: true},
		{Selector: "Y)", Label: "Yes"},
		{Selector: "N)", Label: "No"},
		{Blank: true},
	})
}

func (s adminUsernameRemove) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "y", "yes":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.transition(p, adminUsernamesList{})
			return
		}
		if err := d.Security.AllowUsername(h.ctx(), s.name); err != nil {
			_ = p.Write(securityErrLine(err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "username_deny_remove", s.name)
		_ = p.Write("Removed \"" + s.name + "\".\r\n")
		h.transition(p, adminUsernamesList{})
	case "n", "no", "b", "back", "..", "":
		h.transition(p, adminUsernamesList{})
	default:
		h.redraw(p)
	}
}

// adminReservedNamesList paginates username_history rows with a release
// action per entry.
type adminReservedNamesList struct{ page int }

func (s adminReservedNamesList) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.Security == nil {
		return frame(h, "admin — Security — Reserved names", append(rows,
			Row{Label: "(security service unavailable)"},
			Row{Blank: true},
			Row{Selector: "B)", Label: "Back"},
			Row{Blank: true},
		))
	}
	hist, err := d.Security.ListUsernameHistory(h.ctx(), 0)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to load: " + err.Error() + ")"})
	case len(hist) == 0:
		rows = append(rows, Row{Label: "(no reserved names)"})
	default:
		start, end := pageWindow(s.page, len(hist))
		for i := start; i < end; i++ {
			r := hist[i]
			when := time.Unix(r.RenamedAt, 0).UTC().Format("2006-01-02")
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    fmt.Sprintf("%-20s → %-20s  (%s)", r.OldUsername, r.RenamedTo, when),
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(hist))
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	return frame(h, "admin — Security — Reserved names", rows) + "Select: "
}

func (s adminReservedNamesList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminSecurity{})
		return
	case "n", "next":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.redraw(p)
			return
		}
		hist, _ := d.Security.ListUsernameHistory(h.ctx(), 0)
		if (s.page+1)*pageSize < len(hist) {
			h.transition(p, adminReservedNamesList{page: s.page + 1})
		} else {
			h.redraw(p)
		}
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminReservedNamesList{page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.redraw(p)
			return
		}
		hist, _ := d.Security.ListUsernameHistory(h.ctx(), 0)
		if n >= 1 && n <= len(hist) {
			h.transition(p, adminReservedNameRelease{name: hist[n-1].OldUsername})
			return
		}
	}
	h.redraw(p)
}

// adminReservedNameRelease confirms releasing one history row.
type adminReservedNameRelease struct{ name string }

func (s adminReservedNameRelease) render(h *Handler, _ *engage.Participant) string {
	return frame(h, "admin — Security — Release", []Row{
		{Blank: true},
		{Label: "Release \"" + s.name + "\"?"},
		{Label: "(future account-create and rename attempts may use this name again.)"},
		{Blank: true},
		{Selector: "Y)", Label: "Yes"},
		{Selector: "N)", Label: "No"},
		{Blank: true},
	})
}

func (s adminReservedNameRelease) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "y", "yes":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.transition(p, adminReservedNamesList{})
			return
		}
		if err := d.Security.ReleaseUsernameHistory(h.ctx(), s.name); err != nil {
			_ = p.Write(securityErrLine(err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "username_history_release", s.name)
		_ = p.Write("Released \"" + s.name + "\".\r\n")
		h.transition(p, adminReservedNamesList{})
	case "n", "no", "b", "back", "..", "":
		h.transition(p, adminReservedNamesList{})
	default:
		h.redraw(p)
	}
}

// adminIPDenialsList paginates the IP deny list with [T]/[P] markers.
type adminIPDenialsList struct{ page int }

func (s adminIPDenialsList) render(h *Handler, _ *engage.Participant) string {
	d := getDeps(h)
	rows := []Row{{Blank: true}}
	if d == nil || d.Security == nil {
		return frame(h, "admin — Security — IP denials", append(rows,
			Row{Label: "(security service unavailable)"},
			Row{Blank: true},
			Row{Selector: "B)", Label: "Back"},
			Row{Blank: true},
		))
	}
	denials := d.Security.ListDenials()
	switch {
	case len(denials) == 0:
		rows = append(rows, Row{Label: "(deny list is empty)"})
	default:
		start, end := pageWindow(s.page, len(denials))
		for i := start; i < end; i++ {
			row := denials[i]
			mark := "[T]"
			expires := ""
			if row.Permanent() {
				mark = "[P]"
				expires = "—"
			} else {
				expires = row.ExpiresAt.Format("2006-01-02 15:04")
			}
			label := fmt.Sprintf("%s %-15s  %s", mark, row.IP.String(), expires)
			if row.Reason != "" {
				label += "  " + row.Reason
			}
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label:    label,
			})
		}
	}
	rows = appendPageControls(rows, s.page, len(denials))
	rows = append(rows,
		Row{Selector: "A)", Label: "Add"},
		Row{Selector: "F)", Label: "Flush temporary"},
		Row{Selector: "B)", Label: "Back"},
		Row{Blank: true},
	)
	return frame(h, "admin — Security — IP denials", rows) + "Select: "
}

func (s adminIPDenialsList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, adminSecurity{})
		return
	case "a", "add":
		h.transition(p, adminIPAdd{})
		return
	case "f", "flush":
		h.transition(p, adminIPFlushConfirm{})
		return
	case "n", "next":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.redraw(p)
			return
		}
		denials := d.Security.ListDenials()
		if (s.page+1)*pageSize < len(denials) {
			h.transition(p, adminIPDenialsList{page: s.page + 1})
		} else {
			h.redraw(p)
		}
		return
	case "p", "prev", "previous":
		if s.page > 0 {
			h.transition(p, adminIPDenialsList{page: s.page - 1})
		} else {
			h.redraw(p)
		}
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.redraw(p)
			return
		}
		denials := d.Security.ListDenials()
		if n >= 1 && n <= len(denials) {
			h.transition(p, adminIPRemoveConfirm{ip: denials[n-1].IP})
			return
		}
	}
	h.redraw(p)
}

// adminIPAdd captures kind + ip [+ ttl] + reason in one line.
type adminIPAdd struct{}

func (adminIPAdd) render(h *Handler, _ *engage.Participant) string {
	return frame(h, "admin — Security — Add IP", []Row{
		{Blank: true},
		{Label: "Type: <ip> permanent [reason]"},
		{Label: "  or: <ip> temp <seconds> [reason]"},
		{Label: "  or: <ip> [reason]            (uses default TTL)"},
		{Label: "/abort cancels."},
		{Blank: true},
	}) + "Add: "
}

func (adminIPAdd) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, adminIPDenialsList{})
		return
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		h.redraw(p)
		return
	}
	addr, err := netip.ParseAddr(parts[0])
	if err != nil {
		_ = p.Write("Invalid IP.\r\n")
		h.redraw(p)
		return
	}
	d := getDeps(h)
	if d == nil || d.Security == nil {
		h.transition(p, adminIPDenialsList{})
		return
	}
	actor, _ := h.accountFor(p)
	var addedBy *int64
	if actor != nil {
		v := actor.ID
		addedBy = &v
	}
	kind := "temp"
	reasonStart := 1
	var ttl time.Duration
	if len(parts) >= 2 {
		switch strings.ToLower(parts[1]) {
		case "permanent", "perm", "p":
			kind = "permanent"
			reasonStart = 2
		case "temp", "t":
			kind = "temp"
			reasonStart = 2
			if len(parts) >= 3 {
				if n, perr := strconv.Atoi(parts[2]); perr == nil && n >= 0 {
					ttl = time.Duration(n) * time.Second
					reasonStart = 3
				}
			}
		}
	}
	var reason string
	if reasonStart < len(parts) {
		reason = strings.Join(parts[reasonStart:], " ")
	}
	switch kind {
	case "permanent":
		if err := d.Security.AddPermanentDeny(h.ctx(), addr, addedBy, reason); err != nil {
			_ = p.Write(securityErrLine(err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "ip_deny_add", addr.String(), "kind", "permanent", "reason", reason)
	default:
		if err := d.Security.AddTempDeny(h.ctx(), addr, ttl, addedBy, reason); err != nil {
			_ = p.Write(securityErrLine(err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "ip_deny_add", addr.String(), "kind", "temp", "ttl", int(ttl.Seconds()), "reason", reason)
	}
	_ = p.Write("Added " + addr.String() + ".\r\n")
	h.transition(p, adminIPDenialsList{})
}

// adminIPRemoveConfirm confirms removing one IP deny row.
type adminIPRemoveConfirm struct{ ip netip.Addr }

func (s adminIPRemoveConfirm) render(h *Handler, _ *engage.Participant) string {
	return frame(h, "admin — Security — Remove IP", []Row{
		{Blank: true},
		{Label: "Remove deny for " + s.ip.String() + "?"},
		{Blank: true},
		{Selector: "Y)", Label: "Yes"},
		{Selector: "N)", Label: "No"},
		{Blank: true},
	})
}

func (s adminIPRemoveConfirm) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "y", "yes":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.transition(p, adminIPDenialsList{})
			return
		}
		if err := d.Security.RemoveDeny(h.ctx(), s.ip); err != nil {
			_ = p.Write(securityErrLine(err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "ip_deny_remove", s.ip.String())
		_ = p.Write("Removed " + s.ip.String() + ".\r\n")
		h.transition(p, adminIPDenialsList{})
	case "n", "no", "b", "back", "..", "":
		h.transition(p, adminIPDenialsList{})
	default:
		h.redraw(p)
	}
}

// adminIPFlushConfirm confirms flushing every temporary deny row.
type adminIPFlushConfirm struct{}

func (adminIPFlushConfirm) render(h *Handler, _ *engage.Participant) string {
	return frame(h, "admin — Security — Flush temp", []Row{
		{Blank: true},
		{Label: "Drop every temporary IP deny? (permanent rows are kept.)"},
		{Blank: true},
		{Selector: "Y)", Label: "Yes"},
		{Selector: "N)", Label: "No"},
		{Blank: true},
	})
}

func (adminIPFlushConfirm) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "y", "yes":
		d := getDeps(h)
		if d == nil || d.Security == nil {
			h.transition(p, adminIPDenialsList{})
			return
		}
		n, err := d.Security.FlushTempDenials(h.ctx())
		if err != nil {
			_ = p.Write(securityErrLine(err))
			h.redraw(p)
			return
		}
		adminAudit(h, p, "ip_deny_flush_temp", "*", "count", n)
		_ = p.Write(fmt.Sprintf("Flushed %d temporary deny(s).\r\n", n))
		h.transition(p, adminIPDenialsList{})
	case "n", "no", "b", "back", "..", "":
		h.transition(p, adminIPDenialsList{})
	default:
		h.redraw(p)
	}
}

// securityErrLine is the menu-layer analogue of formatSecurityErr in
// world/cmd: render security sentinels with a stable, user-facing line.
func securityErrLine(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, security.ErrUsernameDisallowed):
		return "error: username is disallowed\r\n"
	case errors.Is(err, security.ErrUsernameReserved):
		return "error: username is reserved\r\n"
	case errors.Is(err, security.ErrDisallowReservedKeyword):
		return "error: cannot disallow the account-create keyword\r\n"
	case errors.Is(err, security.ErrDisallowExistingAccount):
		return "error: name collides with an existing account\r\n"
	case errors.Is(err, auth.ErrUsernameTaken):
		return "error: username already taken\r\n"
	case errors.Is(err, auth.ErrInvalidUsername):
		return "error: invalid username\r\n"
	}
	return "error: " + err.Error() + "\r\n"
}

// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// Handler is the engage.Handler for KindMenuTerminal hosts. It renders
// a sequence of line-drawn frames over the existing M6 services. Width
// defaults to DefaultWidth; callers may override with SetWidth before
// or during the engagement.
//
// Capacity is 1 (one participant per host) as in M5.7. The per-state
// mutable fields are guarded by mu because main.go's setup path may
// call SetDeps / SetWidth / SetDisengage from a goroutine other than
// the one driving Handle for the engagement.
type Handler struct {
	host    *engage.Host
	deps    *engage.TerminalDeps
	onClose func()

	mu          sync.Mutex
	state       state
	width       int
	height      int
	disengage   func()
	participant *engage.Participant
}

// NewHandler constructs a handler bound to host. onClose, if non-nil,
// is invoked from OnClose (used to broadcast the exit message into the
// room). The handler starts in the main-menu state on OnOpen.
func NewHandler(host *engage.Host, onClose func()) *Handler {
	return &Handler{
		host:    host,
		onClose: onClose,
		width:   DefaultWidth,
		state:   mainMenu{},
	}
}

// SetDeps attaches the service dependencies (mail, boards, files). nil
// makes feature submenus print stub lines instead of touching the
// services. May be called after construction.
func (h *Handler) SetDeps(d *engage.TerminalDeps) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deps = d
}

// SetDisengage installs the callback the handler invokes when the user
// picks the "Quit" entry on the main menu. Wired by main.go to
// engage.CloseForSession; left nil in tests that only exercise rendering.
func (h *Handler) SetDisengage(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disengage = fn
}

// SetWidth overrides the frame width. Values outside [MinWidth, MaxWidth]
// are clamped at render time. A non-positive argument is ignored.
func (h *Handler) SetWidth(w int) {
	if w <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.width = w
}

// SetHeight records the negotiated terminal height. M6.3 does not yet
// consume the value at render time; the field is plumbed so future
// height-sensitive views (paged thread, the M6.4 admin menu) can read
// it without another wire-up pass. Non-positive values are ignored.
func (h *Handler) SetHeight(n int) {
	if n <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.height = n
}

// Resize implements engage.Resizer. It updates the stored dimensions
// for any future render and, if a participant is currently attached,
// re-renders the active frame so the player sees the new size right
// away. Either argument may be 0 to mean "no change on this axis".
//
// The dimension write happens under h.mu, then the lock is released
// before calling redraw (which re-acquires h.mu to read the current
// state). A concurrent Handle that transitions state between the
// unlock and the redraw will be reflected in the redrawn frame — see
// the "Snapshot semantics" note on engage.Resizer. The behaviour is
// the right one for this handler (you always want to see the latest
// state at the new size), but anything more elaborate should think
// twice before relying on a state-vs-size happens-before order.
func (h *Handler) Resize(width, height int) {
	h.mu.Lock()
	if width > 0 {
		h.width = width
	}
	if height > 0 {
		h.height = height
	}
	p := h.participant
	h.mu.Unlock()
	if p != nil {
		h.redraw(p)
	}
}

// OnOpen renders the initial frame and stashes the participant for
// state transitions to use during render. Capacity-1 hosts only see one
// participant for the lifetime of the engagement.
func (h *Handler) OnOpen(p *engage.Participant) {
	h.mu.Lock()
	h.participant = p
	h.mu.Unlock()
	h.redraw(p)
}

// OnClose fires the optional close callback.
func (h *Handler) OnClose(_ *engage.Participant, _ engage.CloseReason) {
	if h.onClose != nil {
		h.onClose()
	}
}

// Handle dispatches one line of input to the current state.
func (h *Handler) Handle(p *engage.Participant, line string) {
	h.mu.Lock()
	if h.participant == nil {
		h.participant = p
	}
	st := h.state
	h.mu.Unlock()
	if st == nil {
		st = mainMenu{}
	}
	st.handle(h, p, line)
}

// transition swaps the current state and redraws. Callers hold no locks
// when invoking transition; redraw takes the lock briefly to copy state.
func (h *Handler) transition(p *engage.Participant, next state) {
	h.mu.Lock()
	h.state = next
	h.mu.Unlock()
	h.redraw(p)
}

// redraw clears the screen and emits the current state's frame.
func (h *Handler) redraw(p *engage.Participant) {
	h.mu.Lock()
	st := h.state
	h.mu.Unlock()
	if st == nil {
		st = mainMenu{}
	}
	_ = p.Write(ClearScreen)
	_ = p.Write(st.render(h, p))
}

// ctx returns the engine root context for handler-initiated DB calls,
// or context.Background as a fallback (test paths without deps).
func (h *Handler) ctx() context.Context {
	h.mu.Lock()
	d := h.deps
	h.mu.Unlock()
	if d != nil && d.RootCtx != nil {
		return d.RootCtx
	}
	return context.Background()
}

// accountFor resolves the participant's account using deps.AccountFor.
// Returns nil, nil if deps or AccountFor is unset.
func (h *Handler) accountFor(p *engage.Participant) (*auth.Account, error) {
	h.mu.Lock()
	d := h.deps
	h.mu.Unlock()
	if d == nil || d.AccountFor == nil {
		return nil, nil
	}
	return d.AccountFor(p.PlayerID)
}

// width0 returns the current frame width under the lock.
func (h *Handler) width0() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.width
}

// ---------------------------------------------------------------------------
// state interface

type state interface {
	render(h *Handler, p *engage.Participant) string
	handle(h *Handler, p *engage.Participant, line string)
}

// ---------------------------------------------------------------------------
// main menu

type mainMenu struct{}

func (mainMenu) render(h *Handler, p *engage.Participant) string {
	entries := visibleMenu(h, p)
	rows := []Row{{Blank: true}}
	for i, e := range entries {
		sel := fmt.Sprintf("%d)", i+1)
		rows = append(rows, Row{
			Selector: sel,
			Label:    featureLabel(e),
			Note:     featureNote(h, p, e),
		})
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "Q)", Label: "Quit"},
		Row{Blank: true},
	)
	f := Frame{Width: h.width0(), Title: "menu", Rows: rows}
	return f.String() + "Select: "
}

func (mainMenu) handle(h *Handler, p *engage.Participant, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		h.redraw(p)
		return
	}
	low := strings.ToLower(line)
	if low == "q" || low == "quit" {
		h.mu.Lock()
		fn := h.disengage
		h.mu.Unlock()
		if fn != nil {
			fn()
		}
		return
	}
	entries := visibleMenu(h, p)
	if n, err := strconv.Atoi(low); err == nil && n >= 1 && n <= len(entries) {
		h.transition(p, newFeatureState(entries[n-1]))
		return
	}
	h.redraw(p)
}

// visibleMenu returns the menu entries the participant is allowed to see.
// Admin entries are filtered out for non-admin participants; the indices
// in the returned slice are the indices the user actually sees, so the
// renderer and handler always agree on numbering. A nil/failed account
// lookup is treated as "non-admin" — fail closed.
func visibleMenu(h *Handler, p *engage.Participant) []engage.MenuEntry {
	all := h.host.Menu
	if !menuHasAdmin(all) {
		return all
	}
	isAdmin := false
	if acc, err := h.accountFor(p); err == nil && acc != nil && acc.AccessLevel == auth.AccessAdmin {
		isAdmin = true
	}
	if isAdmin {
		return all
	}
	out := make([]engage.MenuEntry, 0, len(all))
	for _, e := range all {
		if e.Feature == engage.FeatureAdmin {
			continue
		}
		out = append(out, e)
	}
	return out
}

func menuHasAdmin(entries []engage.MenuEntry) bool {
	for _, e := range entries {
		if e.Feature == engage.FeatureAdmin {
			return true
		}
	}
	return false
}

// featureLabel returns the human-readable label for a menu entry.
func featureLabel(e engage.MenuEntry) string {
	switch e.Feature {
	case engage.FeatureMail:
		return "Mail"
	case engage.FeatureBoards:
		return "Message Boards"
	case engage.FeatureFiles:
		if e.Area != "" {
			return "Files — " + e.Area
		}
		return "Files"
	case engage.FeatureAdmin:
		return "Admin"
	}
	return e.Feature
}

// featureNote returns the right-aligned annotation (e.g. "(3 unread)")
// for a menu entry. Best-effort: returns empty string when the relevant
// service is unset or the lookup fails.
func featureNote(h *Handler, p *engage.Participant, e engage.MenuEntry) string {
	if e.Feature != engage.FeatureMail {
		return ""
	}
	h.mu.Lock()
	d := h.deps
	h.mu.Unlock()
	if d == nil || d.Mail == nil || d.AccountFor == nil || p == nil {
		return ""
	}
	acc, err := d.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		return ""
	}
	n, err := d.Mail.UnreadCount(h.ctx(), acc.ID)
	if err != nil || n == 0 {
		return ""
	}
	return fmt.Sprintf("(%d unread)", n)
}

// newFeatureState picks the submenu state for a given menu entry.
// The set of accepted features is closed and enforced by
// engage.ValidateMenu at set_engage time, so an unknown Feature here
// is a programming error (someone added a Feature* constant without
// extending the switch) rather than a config bug — hence the panic
// instead of a soft "not yet implemented" fallback.
func newFeatureState(e engage.MenuEntry) state {
	switch e.Feature {
	case engage.FeatureMail:
		return mailInbox{}
	case engage.FeatureBoards:
		return boardsList{}
	case engage.FeatureFiles:
		return filesList{area: e.Area}
	case engage.FeatureAdmin:
		return adminMain{}
	default:
		panic(fmt.Sprintf("menu: unknown feature %q after ValidateMenu", e.Feature))
	}
}

// isBackInput reports whether line means "return to the parent menu".
// Accepts "B", "b", "back", "..", and empty (treated as redraw, not back).
func isBackInput(line string) bool {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "b", "back", "..":
		return true
	}
	return false
}

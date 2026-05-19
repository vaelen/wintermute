// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// tokenTTL is how long upload/download URLs stay valid. Matches the
// terminal handler's default.
const tokenTTL = 5 * time.Minute

// ---------------------------------------------------------------------------
// filesList: paginated list of files in a single area.

type filesList struct {
	area string
	page int
}

func (s filesList) render(h *Handler, p *engage.Participant) string {
	rows := []Row{{Blank: true}}
	list, err := loadFiles(h, p, s.area)
	switch {
	case err != nil:
		rows = append(rows, Row{Label: "(unable to list files: " + err.Error() + ")"})
	case len(list) == 0:
		rows = append(rows, Row{Label: "(area is empty)"})
	default:
		start := s.page * pageSize
		end := start + pageSize
		if end > len(list) {
			end = len(list)
		}
		if start >= len(list) {
			start, end = 0, 0
		}
		for i := start; i < end; i++ {
			ff := list[i]
			rows = append(rows, Row{
				Selector: fmt.Sprintf("%d)", i+1),
				Label: fmt.Sprintf("%s (%s)",
					truncRune(ff.Slug, 24),
					humanBytes(ff.Size)),
			})
		}
	}
	rows = append(rows,
		Row{Blank: true},
		Row{Selector: "U)", Label: "Upload"},
	)
	if s.page > 0 {
		rows = append(rows, Row{Selector: "P)", Label: "Previous page"})
	}
	if (s.page+1)*pageSize < len(list) {
		rows = append(rows, Row{Selector: "N)", Label: "Next page"})
	}
	rows = append(rows, Row{Selector: "B)", Label: "Back"}, Row{Blank: true})
	f := Frame{Width: h.width0(), Title: "Files — " + s.area, Rows: rows}
	return f.String() + "Select: "
}

func (s filesList) handle(h *Handler, p *engage.Participant, line string) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch low {
	case "":
		h.redraw(p)
		return
	case "b", "back", "..":
		h.transition(p, mainMenu{})
		return
	case "u", "upload":
		h.transition(p, fileUploadSlug{parent: s})
		return
	case "n", "next":
		h.transition(p, filesList{area: s.area, page: s.page + 1})
		return
	case "p", "prev", "previous":
		next := s.page - 1
		if next < 0 {
			next = 0
		}
		h.transition(p, filesList{area: s.area, page: next})
		return
	}
	if n, err := strconv.Atoi(low); err == nil {
		list, ferr := loadFiles(h, p, s.area)
		if ferr == nil && n >= 1 && n <= len(list) {
			s.issueDownload(h, p, list[n-1])
			return
		}
	}
	h.redraw(p)
}

// issueDownload mints a download token and renders the URL inside the
// current frame (no state change).
func (s filesList) issueDownload(h *Handler, p *engage.Participant, ff files.File) {
	d, acc, ok := filesReady(h, p)
	if !ok {
		h.redraw(p)
		return
	}
	tok, err := d.Files.IssueDownload(h.ctx(), acc.ID, ff.ID, tokenTTL)
	if err != nil {
		_ = p.Write(fmt.Sprintf("download: %v\r\n", err))
		return
	}
	url := d.DownloadURL(tok.Value)
	_ = p.Write(fmt.Sprintf("\r\nDownload URL (valid for 5 minutes):\r\n  %s\r\n\r\n", url))
	// Re-render the list so the user can pick again or go back.
	h.redraw(p)
}

// loadFiles lists every file in the configured area regardless of owner.
// Areas are intentionally shared file pools (M6.2 design), so a player
// browsing a kiosk's area sees — and can issue download tokens for —
// any account's contributions there. Per-file ACLs are still enforced
// by the files service; area-level visibility is wholly the area's
// read_min_level band.
func loadFiles(h *Handler, p *engage.Participant, area string) ([]files.File, error) {
	d, _, ok := filesReady(h, p)
	if !ok {
		return nil, nil
	}
	return d.Files.ListFiles(h.ctx(), files.ListFilter{Area: area})
}

func filesReady(h *Handler, p *engage.Participant) (*engage.TerminalDeps, *auth.Account, bool) {
	h.mu.Lock()
	d := h.deps
	h.mu.Unlock()
	if d == nil || d.Files == nil || d.AccountFor == nil ||
		d.UploadURL == nil || d.DownloadURL == nil || p == nil {
		return nil, nil, false
	}
	acc, err := d.AccountFor(p.PlayerID)
	if err != nil || acc == nil {
		return nil, nil, false
	}
	return d, acc, true
}

// ---------------------------------------------------------------------------
// fileUploadSlug: prompts the user for a slug, then mints an upload URL.

type fileUploadSlug struct{ parent filesList }

func (s fileUploadSlug) render(h *Handler, _ *engage.Participant) string {
	rows := []Row{
		{Blank: true},
		{Label: "Upload to area: " + s.parent.area},
		{Label: "Type the slug for the new file, or /abort to cancel."},
		{Blank: true},
	}
	f := Frame{Width: h.width0(), Title: "Files — Upload", Rows: rows}
	return f.String() + "Slug: "
}

func (s fileUploadSlug) handle(h *Handler, p *engage.Participant, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		h.redraw(p)
		return
	}
	if trimmed == "/abort" {
		h.transition(p, s.parent)
		return
	}
	d, acc, ok := filesReady(h, p)
	if !ok {
		h.transition(p, s.parent)
		return
	}
	tok, err := d.Files.IssueUpload(h.ctx(), acc.ID, trimmed, s.parent.area, tokenTTL)
	if err != nil {
		_ = p.Write(fmt.Sprintf("upload: %v\r\n", err))
		h.transition(p, s.parent)
		return
	}
	url := d.UploadURL(tok.Value)
	_ = p.Write(fmt.Sprintf(
		"\r\nUpload URL (valid for 5 minutes):\r\n  %s\r\n\r\nExample: curl --upload-file <local> %s\r\n\r\n",
		url, url))
	h.transition(p, s.parent)
}

// ---------------------------------------------------------------------------
// helpers

// humanBytes renders n bytes as a short human-readable size.
func humanBytes(n int64) string {
	const (
		kb = 1 << 10
		mb = 1 << 20
		gb = 1 << 30
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	}
	return fmt.Sprintf("%d B", n)
}

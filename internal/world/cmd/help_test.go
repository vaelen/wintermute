// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
)

// help-output tokens that distinguish the three access tiers. The
// "World building" header gates the build subset (admin OR builder);
// "NPCs (admin)" gates the admin-only subset.
const (
	helpBuildHeader = "World building (admin or builder)"
	helpAdminHeader = "NPCs (admin)"
)

func TestHelpPlayerOmitsAdminSections(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Presence.Account.AccessLevel = auth.AccessPlayer
	if got := h.Dispatch(context.Background(), "help"); got != OutcomeContinue {
		t.Fatalf("Dispatch help = %v", got)
	}
	out := rw.Drain()
	if !strings.Contains(out, "Commands available:") {
		t.Errorf("missing base help: %q", out)
	}
	if strings.Contains(out, helpBuildHeader) {
		t.Errorf("player should not see build section: %q", out)
	}
	if strings.Contains(out, helpAdminHeader) {
		t.Errorf("player should not see admin section: %q", out)
	}
}

func TestHelpBuilderShowsBuildSectionOnly(t *testing.T) {
	h, rw := newHandler(t, "builder")
	h.Presence.Account.AccessLevel = auth.AccessBuilder
	if got := h.Dispatch(context.Background(), "help"); got != OutcomeContinue {
		t.Fatalf("Dispatch help = %v", got)
	}
	out := rw.Drain()
	if !strings.Contains(out, helpBuildHeader) {
		t.Errorf("builder should see build section: %q", out)
	}
	if strings.Contains(out, helpAdminHeader) {
		t.Errorf("builder should not see admin section: %q", out)
	}
	if !strings.Contains(out, "@create-room") {
		t.Errorf("build section should list @create-room: %q", out)
	}
}

func TestHelpAdminShowsEverything(t *testing.T) {
	h, rw, _ := newAdminHandler(t)
	if got := h.Dispatch(context.Background(), "help"); got != OutcomeContinue {
		t.Fatalf("Dispatch help = %v", got)
	}
	out := rw.Drain()
	for _, want := range []string{
		"Commands available:",
		helpBuildHeader,
		helpAdminHeader,
		"@create-room",
		"@create-npc",
		"@edit",
		"@tools",
		"@boot",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("admin help missing %q in:\n%s", want, out)
		}
	}
}

func TestAtHelpInvisibleToPlayers(t *testing.T) {
	h, rw := newHandler(t, "alice")
	h.Presence.Account.AccessLevel = auth.AccessPlayer
	if got := h.Dispatch(context.Background(), "@help"); got != OutcomeUnknown {
		t.Errorf("non-admin @help should be OutcomeUnknown, got %v", got)
	}
	if out := rw.Drain(); out != "" {
		t.Errorf("non-admin @help should produce no output, got %q", out)
	}
}

func TestAtHelpForAdminMatchesHelp(t *testing.T) {
	h, rw, _ := newAdminHandler(t)
	if got := h.Dispatch(context.Background(), "@help"); got != OutcomeContinue {
		t.Fatalf("admin @help = %v", got)
	}
	out := rw.Drain()
	if !strings.Contains(out, helpAdminHeader) {
		t.Errorf("admin @help did not include admin section: %q", out)
	}
}

func TestAtHelpForBuilderShowsBuildOnly(t *testing.T) {
	// Use the admin-wired handler so AdminBackend is present, then demote.
	h, rw, _ := newAdminHandler(t)
	h.Presence.Account.AccessLevel = auth.AccessBuilder
	if got := h.Dispatch(context.Background(), "@help"); got != OutcomeContinue {
		t.Fatalf("builder @help = %v", got)
	}
	out := rw.Drain()
	if !strings.Contains(out, helpBuildHeader) {
		t.Errorf("builder @help should include build section: %q", out)
	}
	if strings.Contains(out, helpAdminHeader) {
		t.Errorf("builder @help should NOT include admin section: %q", out)
	}
}

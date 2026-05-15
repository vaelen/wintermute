// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"strings"
	"testing"
)

func TestAtEditPasteThenRunCreatesNPC(t *testing.T) {
	h, rw, _ := newAdminHandler(t)

	// Open the editor.
	if got := h.Dispatch(context.Background(), "@edit init.0010-bartender"); got != OutcomeContinue {
		t.Fatalf("@edit = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "End input") {
		t.Errorf("missing editor prompt: %q", out)
	}

	// Paste a script line by line.
	for _, line := range []string{
		`wintermute.npc.ensure({`,
		`    slug = "test/bartender",`,
		`    name = "the bartender",`,
		`    room = "lobby",`,
		`    persona = "A weary fixer who keeps things short.",`,
		`})`,
		`.`,
	} {
		h.Dispatch(context.Background(), line)
	}
	if out := rw.Drain(); !strings.Contains(out, "Saved") {
		t.Errorf("missing save confirmation: %q", out)
	}

	// Re-running an init script that uses ensure should be a no-op
	// (no duplicate_slug error) — covers acceptance criterion 6.
	if got := h.Dispatch(context.Background(), "@run init.0010-bartender"); got != OutcomeContinue {
		t.Fatalf("@run = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "ran") {
		t.Errorf("first @run did not confirm: %q", out)
	}
	if got := h.Dispatch(context.Background(), "@run init.0010-bartender"); got != OutcomeContinue {
		t.Fatalf("@run #2 = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "ran") {
		t.Errorf("second @run should succeed via ensure idempotency: %q", out)
	}
}

func TestAtEditAbortCancelsSave(t *testing.T) {
	h, rw, _ := newAdminHandler(t)
	h.Dispatch(context.Background(), "@edit init.aborted")
	rw.Drain()
	h.Dispatch(context.Background(), `wintermute.system.log("never persisted")`)
	h.Dispatch(context.Background(), ".abort")
	if out := rw.Drain(); !strings.Contains(out, "Aborted") {
		t.Errorf("missing abort confirmation: %q", out)
	}
	if _, err := h.Admin.Scripts.Get(context.Background(), "init.aborted"); err == nil {
		t.Errorf("aborted script should not be saved")
	}
}

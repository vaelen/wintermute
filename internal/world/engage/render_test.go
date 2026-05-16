// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import "testing"

func TestExpandTemplate(t *testing.T) {
	cases := []struct {
		tmpl, player, host, want string
	}{
		{"{{player}} sits down at {{host}}.", "alice", "the terminal",
			"alice sits down at the terminal."},
		{"{{player}} turns to {{host}}.", "Bob", "Bartender",
			"Bob turns to Bartender."},
		{"no placeholders here", "x", "y", "no placeholders here"},
		{"", "x", "y", ""},
	}
	for _, c := range cases {
		got := ExpandTemplate(c.tmpl, c.player, c.host)
		if got != c.want {
			t.Errorf("ExpandTemplate(%q, %q, %q) = %q; want %q",
				c.tmpl, c.player, c.host, got, c.want)
		}
	}
}

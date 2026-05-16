// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import "testing"

func TestMatchEngageVerb_prefix(t *testing.T) {
	hosts := []*Host{
		{ObjectID: 1, Kind: KindTerminal, EngageVerbs: []string{"use", "sit at"}},
		{ObjectID: 2, Kind: KindNPC, EngageVerbs: []string{"talk to", "address"}},
	}
	cases := []struct {
		line       string
		wantHost   *Host
		wantTarget string
	}{
		{"sit at the terminal", hosts[0], "the terminal"},
		{"use terminal", hosts[0], "terminal"},
		{"talk to bartender", hosts[1], "bartender"},
		{"address the bartender", hosts[1], "the bartender"},
		{"engage with mirror", nil, ""}, // universal verb tested separately
	}
	for _, c := range cases {
		h, target, ok := MatchEngageVerb(c.line, hosts)
		if !ok {
			if c.wantHost == nil {
				continue
			}
			t.Errorf("MatchEngageVerb(%q): no match, wanted host %d", c.line, c.wantHost.ObjectID)
			continue
		}
		if c.wantHost == nil {
			t.Errorf("MatchEngageVerb(%q): matched host %d, wanted no match", c.line, h.ObjectID)
			continue
		}
		if h.ObjectID != c.wantHost.ObjectID || target != c.wantTarget {
			t.Errorf("MatchEngageVerb(%q) = (host=%d, target=%q); want (host=%d, target=%q)",
				c.line, h.ObjectID, target, c.wantHost.ObjectID, c.wantTarget)
		}
	}
}

func TestMatchEngageVerb_caseInsensitive(t *testing.T) {
	hosts := []*Host{{ObjectID: 1, Kind: KindTerminal, EngageVerbs: []string{"sit at"}}}
	h, target, ok := MatchEngageVerb("Sit At The Terminal", hosts)
	if !ok || h.ObjectID != 1 || target != "The Terminal" {
		t.Errorf("case-insensitive verb match failed: ok=%v host=%v target=%q", ok, h, target)
	}
}

func TestMatchUniversalEngage(t *testing.T) {
	cases := []struct {
		line       string
		wantTarget string
		wantOK     bool
	}{
		{"engage terminal", "terminal", true},
		{"engage with the terminal", "the terminal", true},
		{"engage", "", false},     // no target
		{"engage with", "", false},
		{"engaged", "", false},    // not a verb match
	}
	for _, c := range cases {
		target, ok := MatchUniversalEngageVerb(c.line)
		if ok != c.wantOK || target != c.wantTarget {
			t.Errorf("MatchUniversalEngageVerb(%q) = (%q, %v); want (%q, %v)",
				c.line, target, ok, c.wantTarget, c.wantOK)
		}
	}
}

func TestMatchDisengageVerb(t *testing.T) {
	host := &Host{Kind: KindTerminal, DisengageVerbs: []string{"stand up", "step away"}}
	cases := []struct {
		line   string
		wantOK bool
	}{
		{"disengage", true},   // universal
		{"stand up", true},
		{"step away", true},
		{"Stand Up", true},    // case-insensitive
		{"stand", false},      // partial
		{"goodbye", false},    // not in this host's list
	}
	for _, c := range cases {
		got := MatchDisengageVerb(c.line, host)
		if got != c.wantOK {
			t.Errorf("MatchDisengageVerb(%q) = %v, want %v", c.line, got, c.wantOK)
		}
	}
}

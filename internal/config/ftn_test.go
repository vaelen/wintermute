// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFTNNetworks(t *testing.T) {
	path := writeCfg(t, `
[[ftn.network]]
slug    = "local"
name    = "Local"
domain  = "local"
addr    = "255:255/255.0"
default = true

[[ftn.network]]
slug    = "fidonet"
name    = "FidoNet"
domain  = "fidonet"
addr    = "1:234/5.0"
`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.FTN.Network) != 2 {
		t.Fatalf("got %d networks, want 2", len(c.FTN.Network))
	}
	if c.FTN.Network[0].Slug != "local" || !c.FTN.Network[0].Default {
		t.Errorf("network[0] = %+v", c.FTN.Network[0])
	}
	if c.FTN.Network[1].Slug != "fidonet" || c.FTN.Network[1].Default {
		t.Errorf("network[1] = %+v", c.FTN.Network[1])
	}
	if c.FTN.Network[1].Addr != "1:234/5.0" {
		t.Errorf("network[1].Addr = %q", c.FTN.Network[1].Addr)
	}
}

func TestLoadFTNNetworks_RejectsBadAddr(t *testing.T) {
	path := writeCfg(t, `
[[ftn.network]]
slug    = "bad"
name    = "Bad"
domain  = "bad"
addr    = "not-an-address"
`)
	if _, err := Load(path); err == nil {
		t.Errorf("expected error for bad addr")
	}
}

func TestLoadFTNNetworks_RejectsDuplicateSlug(t *testing.T) {
	path := writeCfg(t, `
[[ftn.network]]
slug = "x"
name = "X"
domain = "x"
addr = "1:1/1.0"

[[ftn.network]]
slug = "x"
name = "X2"
domain = "y"
addr = "2:2/2.0"
`)
	if _, err := Load(path); err == nil {
		t.Errorf("expected error for duplicate slug")
	}
}

func TestLoadFTNNetworks_RejectsDuplicateDomain(t *testing.T) {
	path := writeCfg(t, `
[[ftn.network]]
slug = "a"
name = "A"
domain = "dup"
addr = "1:1/1.0"

[[ftn.network]]
slug = "b"
name = "B"
domain = "dup"
addr = "2:2/2.0"
`)
	if _, err := Load(path); err == nil {
		t.Errorf("expected error for duplicate domain")
	}
}

func TestLoadFTNNetworks_RejectsMultipleDefaults(t *testing.T) {
	path := writeCfg(t, `
[[ftn.network]]
slug = "a"
name = "A"
domain = "a"
addr = "1:1/1.0"
default = true

[[ftn.network]]
slug = "b"
name = "B"
domain = "b"
addr = "2:2/2.0"
default = true
`)
	if _, err := Load(path); err == nil {
		t.Errorf("expected error for multiple defaults")
	}
}

func TestLoadFTNNetworks_RequiresSlug(t *testing.T) {
	path := writeCfg(t, `
[[ftn.network]]
name = "Nameless"
domain = "x"
addr = "1:1/1.0"
`)
	if _, err := Load(path); err == nil {
		t.Errorf("expected error for missing slug")
	}
}

func TestLoadFTNNetworks_EmptyIsOK(t *testing.T) {
	// Empty/missing FTN section is fine — bootstrap will auto-create `local`.
	path := writeCfg(t, `[server]
telnet_port = 2323
`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.FTN.Network) != 0 {
		t.Errorf("expected zero networks, got %d", len(c.FTN.Network))
	}
}

// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Server.TelnetPort == 0 {
		t.Errorf("Default() should set a telnet port; got 0")
	}
	if c.DB.Path == "" {
		t.Errorf("Default() should set db.path")
	}
	if c.TLS.Mode != "self-signed" {
		t.Errorf("Default() TLS mode = %q, want self-signed", c.TLS.Mode)
	}
}

func TestLoadEmpty(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") err = %v", err)
	}
	if c.Server.TelnetPort != Default().Server.TelnetPort {
		t.Errorf("Load(\"\") should return defaults")
	}
}

func TestLoadOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	contents := `
[server]
telnet_port = 1234

[db]
path = "/var/lib/wintermute.db"

[log]
level = "debug"

[tls]
mode = "self-signed"
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.TelnetPort != 1234 {
		t.Errorf("telnet_port = %d, want 1234", c.Server.TelnetPort)
	}
	if c.DB.Path != "/var/lib/wintermute.db" {
		t.Errorf("db.path = %q, want /var/lib/wintermute.db", c.DB.Path)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level = %q, want debug", c.Log.Level)
	}
	if c.Server.TLSPort != Default().Server.TLSPort {
		t.Errorf("tls_port should fall back to default, got %d", c.Server.TLSPort)
	}
}

func TestLoadInvalidTLSMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[tls]
mode = "nonsense"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("Load: expected error for invalid tls.mode")
	}
}

func TestLoadAutocertRequiresHostnames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[tls]
mode = "autocert"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("Load: expected error for autocert with no hostnames")
	}
}

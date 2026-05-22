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

func TestDefaultHistorySize(t *testing.T) {
	c := Default()
	if c.Session.HistorySize != 100 {
		t.Errorf("Default() session.history_size = %d, want 100", c.Session.HistorySize)
	}
}

func TestLoadHistorySizeOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[session]
history_size = 0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Session.HistorySize != 0 {
		t.Errorf("session.history_size = %d, want 0", c.Session.HistorySize)
	}
}

func TestLoadHistorySizeRejectsNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[session]
history_size = -1
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("Load: expected error for negative history_size")
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

func TestSecurityDefaults(t *testing.T) {
	c := Default()
	if !c.Security.AutoDenyEnabled {
		t.Errorf("Default() security.auto_deny_enabled = false, want true")
	}
	if c.Security.AutoDenyTTLSeconds != 300 {
		t.Errorf("auto_deny_ttl_seconds = %d, want 300", c.Security.AutoDenyTTLSeconds)
	}
	if c.Security.FailedPasswordThreshold != 5 {
		t.Errorf("failed_password_threshold = %d, want 5", c.Security.FailedPasswordThreshold)
	}
	if c.Security.Filter.Enabled {
		t.Errorf("filter.enabled = true, want false (off by default)")
	}
	if c.Security.Filter.Address != "127.0.0.1:1234" {
		t.Errorf("filter.address = %q, want 127.0.0.1:1234", c.Security.Filter.Address)
	}
}

func TestLoadSecurityNegativeTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[security]
auto_deny_ttl_seconds = -1
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("Load: expected error for negative ttl")
	}
}

func TestLoadSecurityFilterInvalidAddress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[security.filter]
enabled = true
address = "not-a-real:address:::"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("Load: expected error for unparseable filter.address")
	}
}

func TestNPCLoopDefaults(t *testing.T) {
	c := Default()
	if c.NPC.Loop.DebounceMs != 800 {
		t.Errorf("npc.loop.debounce_ms = %d, want 800", c.NPC.Loop.DebounceMs)
	}
	if c.NPC.Loop.MaxToolDepth != 3 {
		t.Errorf("npc.loop.max_tool_depth = %d, want 3", c.NPC.Loop.MaxToolDepth)
	}
	if c.NPC.Loop.DefaultMinuteLimit != 5000 {
		t.Errorf("npc.loop.default_minute_limit = %d, want 5000", c.NPC.Loop.DefaultMinuteLimit)
	}
	if c.NPC.Loop.DefaultHourLimit != 100000 {
		t.Errorf("npc.loop.default_hour_limit = %d, want 100000", c.NPC.Loop.DefaultHourLimit)
	}
	if c.NPC.Loop.DefaultDayLimit != 1000000 {
		t.Errorf("npc.loop.default_day_limit = %d, want 1000000", c.NPC.Loop.DefaultDayLimit)
	}
	if c.NPC.Loop.DefaultGateModel != "llama3.2:1b" {
		t.Errorf("npc.loop.default_gate_model = %q, want llama3.2:1b", c.NPC.Loop.DefaultGateModel)
	}
}

func TestLoadNPCLoopMissingSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[server]
telnet_port = 1234
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := Default()
	if c.NPC.Loop != d.NPC.Loop {
		t.Errorf("npc.loop = %+v, want defaults %+v", c.NPC.Loop, d.NPC.Loop)
	}
}

func TestLoadNPCLoopPartialOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	contents := `
[npc.loop]
debounce_ms = 500
default_gate_model = "qwen2.5:0.5b"
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.NPC.Loop.DebounceMs != 500 {
		t.Errorf("debounce_ms = %d, want 500", c.NPC.Loop.DebounceMs)
	}
	if c.NPC.Loop.DefaultGateModel != "qwen2.5:0.5b" {
		t.Errorf("default_gate_model = %q, want qwen2.5:0.5b", c.NPC.Loop.DefaultGateModel)
	}
	if c.NPC.Loop.MaxToolDepth != 3 {
		t.Errorf("max_tool_depth = %d, want default 3", c.NPC.Loop.MaxToolDepth)
	}
	if c.NPC.Loop.DefaultMinuteLimit != 5000 {
		t.Errorf("default_minute_limit = %d, want default 5000", c.NPC.Loop.DefaultMinuteLimit)
	}
	if c.NPC.Loop.DefaultHourLimit != 100000 {
		t.Errorf("default_hour_limit = %d, want default 100000", c.NPC.Loop.DefaultHourLimit)
	}
	if c.NPC.Loop.DefaultDayLimit != 1000000 {
		t.Errorf("default_day_limit = %d, want default 1000000", c.NPC.Loop.DefaultDayLimit)
	}
}

func TestLoadSecurityFilterValidAddress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wintermute.toml")
	if err := os.WriteFile(path, []byte(`[security.filter]
enabled = true
address = "10.20.30.40:9999"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Security.Filter.Address != "10.20.30.40:9999" {
		t.Errorf("filter.address = %q", c.Security.Filter.Address)
	}
}

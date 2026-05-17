// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"

	"github.com/vaelen/wintermute/internal/ftn/addr"
)

// Config is the engine's top-level configuration as loaded from
// wintermute.toml.
type Config struct {
	Server  ServerConfig  `toml:"server"`
	DB      DBConfig      `toml:"db"`
	Log     LogConfig     `toml:"log"`
	TLS     TLSConfig     `toml:"tls"`
	LLM     LLMConfig     `toml:"llm"`
	Session SessionConfig `toml:"session"`
	FTN     FTNConfig     `toml:"ftn"`
}

// FTNConfig holds the operator-declared FidoNet Technology Network nodes
// this server is a member of. Each entry becomes (or updates) a row in
// the ftn_networks table at startup. An entry for the `local` network is
// auto-created at startup if no networks are declared.
type FTNConfig struct {
	Network []FTNNetwork `toml:"network"`
}

// FTNNetwork is one [[ftn.network]] block.
type FTNNetwork struct {
	Slug    string `toml:"slug"`
	Name    string `toml:"name"`
	Domain  string `toml:"domain"`
	Addr    string `toml:"addr"` // our 5D address in the network, e.g. "1:234/5.0"
	Default bool   `toml:"default"`
}

// ServerConfig holds the listener ports and externally-visible hostname.
type ServerConfig struct {
	TelnetPort int    `toml:"telnet_port"`
	TLSPort    int    `toml:"tls_port"`
	HTTPPort   int    `toml:"http_port"` // unused until M6
	PublicHost string `toml:"public_host"`
}

// DBConfig configures the SQLite database location.
type DBConfig struct {
	Path string `toml:"path"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	// Level is one of "debug", "info", "warn", "error". Default "info".
	Level string `toml:"level"`
	// Format is "text" or "json". Default "text".
	Format string `toml:"format"`
}

// TLSConfig configures certificate provisioning for the TLS listener.
type TLSConfig struct {
	// Mode is one of:
	//   "self-signed" — generate (or load) a persistent dev cert
	//   "autocert"    — Let's Encrypt via ACME
	//   "files"       — read CertPath/KeyPath
	Mode      string   `toml:"mode"`
	CertPath  string   `toml:"cert_path"`
	KeyPath   string   `toml:"key_path"`
	CacheDir  string   `toml:"cache_dir"`
	Hostnames []string `toml:"hostnames"`
}

// LLMConfig parses the LLM backend section. Used from milestone 3 onward;
// parsed but ignored in milestone 1.
type LLMConfig struct {
	Default LLMBackend `toml:"default"`
}

// SessionConfig holds per-session tuning knobs.
type SessionConfig struct {
	// HistorySize is the cap for the in-memory line-editing history ring
	// buffer. Default 100. Set to 0 to disable in-line history.
	HistorySize int `toml:"history_size"`
}

// LLMBackend names a registered backend plus its free-form options.
type LLMBackend struct {
	Backend string         `toml:"backend"`
	Opts    map[string]any `toml:"opts"`
}

// Default returns a Config populated with development-friendly defaults.
// Used both as a fallback when no file is provided and as a baseline that
// Load merges file values into.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			TelnetPort: 2323,
			TLSPort:    2424,
			HTTPPort:   8443,
			PublicHost: "localhost",
		},
		DB: DBConfig{
			Path: "wintermute.db",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
		TLS: TLSConfig{
			Mode: "self-signed",
		},
		LLM: LLMConfig{
			Default: LLMBackend{
				Backend: "ollama",
				Opts: map[string]any{
					"url":              "http://localhost:11434",
					"model":            "llama3.2:3b",
					"gate_model":       "llama3.2:1b",
					"summarizer_model": "llama3.2:1b",
					"embedding_model":  "nomic-embed-text",
				},
			},
		},
		Session: SessionConfig{
			HistorySize: 100,
		},
	}
}

// Load reads a TOML file from path and merges it on top of Default().
// If path is empty, the defaults are returned unmodified.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Server.TelnetPort < 0 || c.Server.TelnetPort > 65535 {
		return fmt.Errorf("server.telnet_port out of range: %d", c.Server.TelnetPort)
	}
	if c.Server.TLSPort < 0 || c.Server.TLSPort > 65535 {
		return fmt.Errorf("server.tls_port out of range: %d", c.Server.TLSPort)
	}
	switch c.TLS.Mode {
	case "self-signed", "autocert", "files":
	default:
		return fmt.Errorf("tls.mode must be one of self-signed|autocert|files, got %q", c.TLS.Mode)
	}
	if c.TLS.Mode == "files" && (c.TLS.CertPath == "" || c.TLS.KeyPath == "") {
		return fmt.Errorf(`tls.mode = "files" requires both tls.cert_path and tls.key_path`)
	}
	if c.TLS.Mode == "autocert" && len(c.TLS.Hostnames) == 0 {
		return fmt.Errorf(`tls.mode = "autocert" requires at least one entry in tls.hostnames`)
	}
	switch c.Log.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be one of debug|info|warn|error, got %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "", "text", "json":
	default:
		return fmt.Errorf("log.format must be one of text|json, got %q", c.Log.Format)
	}
	if c.Session.HistorySize < 0 {
		return fmt.Errorf("session.history_size must be >= 0, got %d", c.Session.HistorySize)
	}
	if err := c.FTN.validate(); err != nil {
		return err
	}
	return nil
}

func (f *FTNConfig) validate() error {
	slugs := map[string]bool{}
	domains := map[string]bool{}
	defaults := 0
	for i, n := range f.Network {
		if n.Slug == "" {
			return fmt.Errorf("ftn.network[%d]: slug required", i)
		}
		if n.Name == "" {
			return fmt.Errorf("ftn.network[%d] (%s): name required", i, n.Slug)
		}
		if n.Domain == "" {
			return fmt.Errorf("ftn.network[%d] (%s): domain required", i, n.Slug)
		}
		if _, err := addr.Parse(n.Addr); err != nil {
			return fmt.Errorf("ftn.network[%d] (%s): addr %q: %w", i, n.Slug, n.Addr, err)
		}
		if slugs[n.Slug] {
			return fmt.Errorf("ftn.network: duplicate slug %q", n.Slug)
		}
		if domains[n.Domain] {
			return fmt.Errorf("ftn.network: duplicate domain %q", n.Domain)
		}
		slugs[n.Slug] = true
		domains[n.Domain] = true
		if n.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("ftn.network: at most one network may have default = true (found %d)", defaults)
	}
	return nil
}

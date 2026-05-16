// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Command wintermute is the entry point for the Wintermute MUD/MUSH server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	_ "github.com/vaelen/wintermute/internal/llm/ollama"
	wnettls "github.com/vaelen/wintermute/internal/net/tls"
	"github.com/vaelen/wintermute/internal/npc"
	scriptlua "github.com/vaelen/wintermute/internal/script/lua"
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
	"github.com/vaelen/wintermute/internal/world/engage"
)

func main() {
	cfgPath := flag.String("config", "", "path to wintermute.toml (optional)")
	flag.Parse()

	if err := run(*cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "wintermute: %v\n", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	logger := buildLogger(cfg.Log)
	logger.Info("starting wintermute",
		"config", cfgPath,
		"telnet_port", cfg.Server.TelnetPort,
		"tls_port", cfg.Server.TLSPort,
		"db", cfg.DB.Path,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DB.Path, logger)
	if err != nil {
		return err
	}
	defer db.Close()

	authStore := auth.NewStore(db)

	w, err := world.Load(ctx, db, logger)
	if err != nil {
		return fmt.Errorf("load world: %w", err)
	}

	// When a new account is created, spawn its body in the world.
	authStore.SetAfterCreate(func(ctx context.Context, acc *auth.Account) error {
		_, err := w.CreatePlayer(ctx, acc)
		return err
	})

	npcReg, err := npc.Load(ctx, db, w, cfg.LLM.Default, logger)
	if err != nil {
		return fmt.Errorf("load npc registry: %w", err)
	}
	w.SetSayObserver(func(roomID world.RoomID, speakerID world.ObjectID, speakerName, text string) {
		npcReg.HandleSay(roomID, speakerID, speakerName, text)
	})
	logger.Info("npc registry loaded")

	// Admin scripting layer (M5): world API, Lua VM pool with the
	// wintermute.* table pre-loaded, tool registry, scripts table access.
	// Wired into the session handler so admin @-commands can mutate the
	// world from inside the game.
	adminAPI := worldapi.New(w, db, authStore, npcReg, logger)
	luaAPI := scriptlua.NewAPI(adminAPI, nil, ctx)
	luaPool := scriptlua.NewPool(scriptlua.PoolConfig{Size: 4, API: luaAPI})
	defer luaPool.Close()
	scripts := scriptlua.NewScriptStore(db)
	adminBackend := &worldcmd.AdminBackend{
		API:     adminAPI,
		Scripts: scripts,
		Pool:    luaPool,
		Tools:   luaAPI.Tools,
	}

	// Run every init.* script in slug order. Failures abort startup so
	// admin-script regressions surface immediately rather than leaving
	// the world in a half-built state.
	if err := scriptlua.RunInit(ctx, luaPool, scripts, func(slug string, err error) {
		if err != nil {
			logger.Error("init script failed", "script", slug, "err", err)
		} else {
			logger.Info("init script", "script", slug, "status", "ok")
		}
	}); err != nil {
		return fmt.Errorf("run init scripts: %w", err)
	}

	// MOTD: prefer the value stored via wintermute.system.motd, falling
	// back to the built-in banner so the engine always has something to
	// show new sessions.
	motd := adminAPI.GetMOTD()
	if motd == "" {
		motd = defaultMOTD()
	}
	// Engagement primitive (M5.7): registry of live engagements, in-memory
	// host cache, and a handler factory that maps host kind to the right
	// built-in handler.
	engageReg := engage.NewRegistry()
	hostCache := engage.NewHostCache()
	if err := hostCache.Load(ctx, db); err != nil {
		return fmt.Errorf("load engage hosts: %w", err)
	}

	// engageOpen is the OpenFn injected into EngageBackend. It constructs the
	// right handler for the host kind, looks up the player's display name, and
	// opens the engagement in the registry. The session-side pointer
	// (s.engagement) is NOT set here — that requires a session lookup that
	// arrives in T13. For now, the registry entry is created but the modal
	// dispatch in commandLoop won't see it until T13 wires OpenForSession.
	// TODO: T13 should use OpenForSession via session lookup here instead.
	engageOpen := func(host *engage.Host, presence *world.Presence) error {
		var handler engage.Handler
		switch host.Kind {
		case engage.KindTerminal:
			handler = engage.NewTerminalHandler(host)
		case engage.KindNPC:
			n := npcReg.Get(host.ObjectID)
			if n == nil {
				return fmt.Errorf("engage: npc %d not in registry", host.ObjectID)
			}
			handler = engage.NewNPCHandler(host, &engage.NPCBinding{
				Client:      npcChatAdapter{n: n},
				DisplayName: n.Name,
				Persona:     n.Persona,
			})
		default:
			return fmt.Errorf("engage: unsupported kind %q", host.Kind)
		}
		displayName := playerNameFor(w, presence.PlayerID)
		p := &engage.Participant{
			SessionID:   presence.SessionID,
			PlayerID:    presence.PlayerID,
			DisplayName: displayName,
			Write:       presence.Write,
		}
		obj, err := w.Object(host.ObjectID)
		if err != nil {
			return fmt.Errorf("engage: lookup host object: %w", err)
		}
		// TODO: T14 broadcast open to room (obj available here for name lookup).
		_ = obj
		_, err = engageReg.Open(host, handler, p)
		return err
	}

	engageBackend := &worldcmd.EngageBackend{
		Registry: engageReg,
		Hosts:    hostCache,
		OpenFn:   engageOpen,
	}

	handler := session.DefaultHandler(authStore, w, npcReg, logger, motd)
	handler.Admin = adminBackend
	handler.HistorySize = cfg.Session.HistorySize
	handler.EngageRegistry = engageReg
	handler.EngageBackend = engageBackend

	// wg tracks BOTH the accept-loop goroutines and every per-session
	// goroutine. On shutdown we Wait on it before letting `defer db.Close()`
	// run, so a session that's mid-write to the DB won't race with the
	// writer goroutine shutting down ("send on closed channel" panic).
	var wg sync.WaitGroup
	if cfg.Server.TelnetPort > 0 {
		ln, err := net.Listen("tcp", joinHostPort("0.0.0.0", cfg.Server.TelnetPort))
		if err != nil {
			return fmt.Errorf("listen tcp: %w", err)
		}
		logger.Info("listening", "kind", "tcp", "addr", ln.Addr().String())
		wg.Add(1)
		go func() {
			defer wg.Done()
			acceptLoop(ctx, ln, handler, logger, &wg)
		}()
		go func() { <-ctx.Done(); _ = ln.Close() }()
	}

	if cfg.Server.TLSPort > 0 {
		tlsLn, err := openTLS(cfg)
		if err != nil {
			return fmt.Errorf("listen tls: %w", err)
		}
		logger.Info("listening", "kind", "tls", "addr", tlsLn.Addr().String(), "mode", cfg.TLS.Mode)
		wg.Add(1)
		go func() {
			defer wg.Done()
			acceptLoop(ctx, tlsLn, handler, logger, &wg)
		}()
		go func() { <-ctx.Done(); _ = tlsLn.Close() }()
	}

	<-ctx.Done()
	logger.Info("shutdown requested")
	// Give in-flight sessions a moment to drain before closing the DB.
	// Per-session goroutines close their underlying conn on ctx cancel
	// (see acceptLoop), so blocked reads inside Handle return quickly
	// and sessions exit promptly. The 2 s timeout is a backstop for any
	// session that is genuinely stuck.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		logger.Warn("forced shutdown after 2s")
	}

	// Drain NPC dispatch goroutines AND memory state before
	// `defer db.Close()` runs. A dispatch holds a per-NPC mutex across
	// an LLM Chat call and then calls into the world and the DB;
	// memory state holds buffered transcripts that still need to be
	// summarised and persisted. Letting db.Close() race with either
	// would leak goroutines and risk "send on closed channel" / SQLite
	// errors. Bound the wait so a hung backend can't pin shutdown.
	//
	// The inner Shutdown ctx is strictly tighter than the outer wait so
	// Shutdown is guaranteed to return first. The outer wait is the
	// belt-and-suspenders for the case where Shutdown itself wedges
	// (it shouldn't — it's context-aware end-to-end — but db.Close()
	// is destructive, so we never want to race it).
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelShutdown()
	npcDone := make(chan struct{})
	go func() {
		if err := npcReg.Shutdown(shutdownCtx); err != nil {
			logger.Warn("npc shutdown returned error", "err", err)
		}
		close(npcDone)
	}()
	select {
	case <-npcDone:
	case <-time.After(5 * time.Second):
		logger.Warn("npc dispatch / memory did not drain within 5s")
	}
	return nil
}

func buildLogger(c config.LogConfig) *slog.Logger {
	var lvl slog.Level
	switch c.Level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if c.Format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

func openTLS(cfg *config.Config) (net.Listener, error) {
	wcfg := wnettls.Config{
		CertPath:  cfg.TLS.CertPath,
		KeyPath:   cfg.TLS.KeyPath,
		CacheDir:  cfg.TLS.CacheDir,
		Hostnames: cfg.TLS.Hostnames,
	}
	switch cfg.TLS.Mode {
	case "self-signed":
		wcfg.Mode = wnettls.ModeSelfSigned
		if wcfg.CertPath == "" {
			home, err := os.UserHomeDir()
			if err == nil {
				wcfg.CertPath = home + "/.wintermute/dev-cert.pem"
				wcfg.KeyPath = home + "/.wintermute/dev-key.pem"
			}
		}
	case "files":
		wcfg.Mode = wnettls.ModeFiles
	case "autocert":
		wcfg.Mode = wnettls.ModeAutocert
	}
	return wnettls.Listen(joinHostPort("0.0.0.0", cfg.Server.TLSPort), wcfg)
}

func joinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func acceptLoop(ctx context.Context, ln net.Listener, h *session.Handler, logger *slog.Logger, wg *sync.WaitGroup) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if errors.Is(err, io.EOF) {
				return
			}
			logger.Warn("accept failed", "err", err)
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("session panic", "err", r, "remote", c.RemoteAddr())
				}
			}()

			// Watch for shutdown and close the underlying conn so any
			// blocked read inside Handle returns promptly. handleDone
			// signals normal completion so the watcher exits without
			// closing the conn twice (Handle's own deferred Close has
			// already run).
			handleDone := make(chan struct{})
			defer close(handleDone)
			go func() {
				select {
				case <-ctx.Done():
					_ = c.Close()
				case <-handleDone:
				}
			}()

			h.Handle(ctx, c)
		}(conn)
	}
}

func defaultMOTD() string {
	return "" +
		"┌───────────────────────────────────────────────┐\r\n" +
		"│           ▓▒░ WINTERMUTE  v0.0 ░▒▓            │\r\n" +
		"│                                               │\r\n" +
		"│  Type 'help' for a list of commands.          │\r\n" +
		"└───────────────────────────────────────────────┘\r\n"
}

// npcChatAdapter wraps an NPC so it satisfies engage.NPCClient.
type npcChatAdapter struct{ n *npc.NPC }

func (a npcChatAdapter) Chat(ctx context.Context, system, user string) (string, error) {
	return a.n.EngageChat(ctx, system, user)
}

// playerNameFor returns the player's display name for engagement rendering.
// Falls back to "someone" if the object cannot be resolved.
func playerNameFor(w *world.World, id world.ObjectID) string {
	obj, err := w.Object(id)
	if err != nil {
		return "someone"
	}
	return obj.Name
}

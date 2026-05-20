// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Command wintermute is the entry point for the Wintermute MUD/MUSH server.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	wintermutehttp "github.com/vaelen/wintermute/internal/http"
	_ "github.com/vaelen/wintermute/internal/llm/ollama"
	"github.com/vaelen/wintermute/internal/mail"
	wnettls "github.com/vaelen/wintermute/internal/net/tls"
	"github.com/vaelen/wintermute/internal/npc"
	scriptlua "github.com/vaelen/wintermute/internal/script/lua"
	"github.com/vaelen/wintermute/internal/security"
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
	"github.com/vaelen/wintermute/internal/world/engage"
	"github.com/vaelen/wintermute/internal/world/engage/menu"
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

	if err := ftnnetworks.Bootstrap(ctx, db, cfg.FTN.Network, logger); err != nil {
		return fmt.Errorf("bootstrap ftn networks: %w", err)
	}

	// M6: mail, boards, files services + MSGID issuer share a single
	// instance across the process.
	msgidIssuer := msgid.NewIssuer(db)
	mailSvc := mail.NewService(db, msgidIssuer, "Wintermute/0.6.0-dev", "")
	boardsSvc := boards.NewService(db, msgidIssuer, boards.ServiceOptions{
		PID:        "Wintermute/0.6.0-dev",
		ServerName: "Wintermute",
		Tearline:   "--- Wintermute/0.6.0-dev",
	})
	filesSvc, err := files.NewService(db, cfg.Files.Root)
	if err != nil {
		return fmt.Errorf("files store: %w", err)
	}

	authStore := auth.NewStore(db)

	// M6.6: login-hardening service. Construct after store.Open so the
	// disallowed-username seed migration has already run, then wire it
	// as the username policy + history recorder on authStore. Errors
	// here are fatal — the engine cannot enforce the deny list without
	// it.
	secSvc, err := buildSecurityService(cfg.Security, db, logger)
	if err != nil {
		return fmt.Errorf("security: %w", err)
	}
	if err := secSvc.Start(ctx); err != nil {
		return fmt.Errorf("security: start: %w", err)
	}
	defer func() { _ = secSvc.Close() }()
	authStore.SetUsernamePolicy(secSvc)
	authStore.SetRenameRecorder(func(ctx context.Context, tx *sql.Tx, oldName string, accountID int64, newName string, renamedBy *int64) error {
		return secSvc.RecordRename(ctx, tx, oldName, accountID, newName, renamedBy)
	})

	w, err := world.Load(ctx, db, logger)
	if err != nil {
		return fmt.Errorf("load world: %w", err)
	}

	// Rename boots every live session for the renamed account so the
	// in-memory s.account.Username cannot drift from the DB. Runs after
	// the rename transaction commits.
	authStore.SetAfterRename(func(_ context.Context, accountID int64, oldName, newName string) error {
		w.BootByAccount(accountID, fmt.Sprintf("Your account has been renamed to %q.\r\n", newName))
		logger.Info("account renamed",
			"account_id", accountID,
			"old_username", oldName,
			"new_username", newName)
		return nil
	})

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

	// Engagement primitive (M5.7): registry of live engagements, in-memory
	// host cache, and a handler factory that maps host kind to the right
	// built-in handler.
	engageReg := engage.NewRegistry()
	npcReg.SetEngageLookup(engageReg)
	hostCache := engage.NewHostCache()
	if err := hostCache.Load(ctx, db); err != nil {
		return fmt.Errorf("load engage hosts: %w", err)
	}

	// Admin scripting layer (M5): world API, Lua VM pool with the
	// wintermute.* table pre-loaded, tool registry, scripts table access.
	// Wired into the session handler so admin @-commands can mutate the
	// world from inside the game.
	adminAPI := worldapi.New(w, db, authStore, npcReg, logger)
	adminAPI.Engage = hostCache
	adminAPI.Mail = mailSvc
	adminAPI.Boards = boardsSvc
	adminAPI.Files = filesSvc
	adminAPI.Security = secSvc
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

	// engageOpen is the OpenFn injected into EngageBackend. It constructs the
	// right handler for the host kind, looks up the player's display name, and
	// opens the engagement in the registry. When a SessionBinding is provided
	// (normal path), OpenForSession is used so the session's engagement pointer
	// is set and modal dispatch in commandLoop activates immediately.
	engageOpen := func(host *engage.Host, presence *world.Presence, sb engage.SessionBinding) error {
		obj, err := w.Object(host.ObjectID)
		if err != nil {
			return fmt.Errorf("engage: lookup host object: %w", err)
		}
		hostName := obj.Name
		displayName := playerNameFor(w, presence.PlayerID)

		// closeBroadcast is passed to the handler and fires on every close
		// reason (voluntary, movement, forced, disconnect).
		closeBroadcast := func() {
			msg := engage.ExpandTemplate(host.ExitMsg, displayName, hostName) + "\r\n"
			loc, locErr := w.LocationOf(host.ObjectID)
			if locErr != nil {
				return
			}
			w.BroadcastToRoom(loc.RoomID, 0, msg)
		}

		var handler engage.Handler
		switch host.Kind {
		case engage.KindTerminal:
			th := engage.NewTerminalHandler(host, closeBroadcast)
			th.SetDeps(terminalDepsWithSecurity(buildTerminalDeps(ctx, w, authStore, mailSvc, boardsSvc, filesSvc, cfg), secSvc, adminAPI))
			handler = th
		case engage.KindMenuTerminal:
			mh := menu.NewHandler(host, closeBroadcast)
			mh.SetDeps(terminalDepsWithSecurity(buildTerminalDeps(ctx, w, authStore, mailSvc, boardsSvc, filesSvc, cfg), secSvc, adminAPI))
			// Size the frame to the client's negotiated terminal width
			// and height. SetWidth / SetHeight no-op on non-positive
			// values, so clients without NAWS keep menu.DefaultWidth and
			// the height stays unset for now. The renderer clamps width
			// to [MinWidth, MaxWidth] at render time.
			mh.SetWidth(presence.TermWidth)
			mh.SetHeight(presence.TermHeight)
			mh.SetDisengage(func() {
				if sb != nil {
					engage.CloseForSession(engageReg, sb, engage.CloseVoluntary)
					return
				}
				if eng := engageReg.HostEngagement(host.ObjectID); eng != nil {
					engageReg.Close(eng, engage.CloseVoluntary)
				}
			})
			handler = mh
		case engage.KindNPC:
			n := npcReg.Get(host.ObjectID)
			if n == nil {
				return fmt.Errorf("engage: npc %d not in registry", host.ObjectID)
			}
			handler = engage.NewNPCHandler(host, &engage.NPCBinding{
				Client:      npcChatAdapter{n: n},
				DisplayName: n.Name,
				Persona:     n.Persona,
				RootCtx:     ctx,
			}, closeBroadcast)
		default:
			return fmt.Errorf("engage: unsupported kind %q", host.Kind)
		}
		p := &engage.Participant{
			SessionID:   presence.SessionID,
			PlayerID:    presence.PlayerID,
			DisplayName: displayName,
			Write:       presence.Write,
		}
		var openErr error
		if sb == nil {
			// No session binding (test path or unsupported caller): fall back
			// to a registry-only open. Modal dispatch in the session loop
			// will not engage without a binding, but this keeps tests
			// compilable.
			_, openErr = engageReg.Open(host, handler, p)
		} else {
			_, openErr = engage.OpenForSession(engageReg, sb, host, handler, p)
		}
		if openErr != nil {
			return openErr
		}
		// Enter broadcast — sent after a successful open.
		enter := engage.ExpandTemplate(host.EnterMsg, displayName, hostName) + "\r\n"
		loc, locErr := w.LocationOf(host.ObjectID)
		if locErr == nil {
			w.BroadcastToRoom(loc.RoomID, 0, enter)
		}
		return nil
	}

	// wg tracks BOTH the accept-loop goroutines and every per-session
	// goroutine. On shutdown we Wait on it before letting `defer db.Close()`
	// run, so a session that's mid-write to the DB won't race with the
	// writer goroutine shutting down ("send on closed channel" panic).
	// Declared early so the BeforeDeleteObserver below can add to it.
	var wg sync.WaitGroup

	// Force-close any live engagement whose host object is being deleted.
	// The hook fires inside w.mu.Lock, so the close is dispatched to a
	// goroutine to avoid deadlocking against OnClose's BroadcastToRoom
	// (which needs w.mu.RLock). The goroutine is tracked in wg so that
	// shutdown's wg.Wait() cannot return before the broadcast completes.
	w.SetBeforeDeleteObserver(func(id world.ObjectID) {
		// Drop the cache entry alongside the DB row (object_engage cascades
		// from objects). Mirrors the DB+cache pairing in api.ClearEngage.
		hostCache.Delete(id)
		eng := engageReg.HostEngagement(id)
		if eng == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			engageReg.Close(eng, engage.CloseForced)
		}()
	})

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
	handler.LoginGuard = secSvc
	handler.PostMOTD = func(ctx context.Context, acc *auth.Account) string {
		n, err := mailSvc.UnreadCount(ctx, acc.ID)
		if err != nil || n == 0 {
			return ""
		}
		s := "s"
		if n == 1 {
			s = ""
		}
		return fmt.Sprintf("You have %d new message%s. Sit at a terminal and type `mail` to read.\r\n", n, s)
	}
	if cfg.Server.TelnetPort > 0 {
		ln, err := net.Listen("tcp", joinHostPort("0.0.0.0", cfg.Server.TelnetPort))
		if err != nil {
			return fmt.Errorf("listen tcp: %w", err)
		}
		logger.Info("listening", "kind", "tcp", "addr", ln.Addr().String())
		wg.Add(1)
		go func() {
			defer wg.Done()
			acceptLoop(ctx, ln, handler, logger, &wg, secSvc)
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
			acceptLoop(ctx, tlsLn, handler, logger, &wg, secSvc)
		}()
		go func() { <-ctx.Done(); _ = tlsLn.Close() }()
	}

	// M6: file-transfer HTTPS listener on cfg.Server.HTTPPort.
	if cfg.Server.HTTPPort > 0 {
		fileHandler := wintermutehttp.NewHandler(filesSvc, wintermutehttp.HandlerOptions{
			MaxUploadBytes: cfg.Files.MaxUploadBytes,
			OnUpload:       uploadMailNotifier(logger, authStore, mailSvc),
			Logger:         logger,
		})
		httpAddr := joinHostPort("0.0.0.0", cfg.Server.HTTPPort)
		// Share the TLS config used by the telnet TLS port: same autocert,
		// same self-signed cert. The underlying net listener is reused
		// only if TLSPort is configured; otherwise we serve plain HTTP
		// (dev-only).
		fileServer := &http.Server{
			Addr:    httpAddr,
			Handler: fileHandler,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("listening", "kind", "http", "addr", httpAddr)
			if err := fileServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("http server", "err", err)
			}
		}()
		go func() { <-ctx.Done(); _ = fileServer.Shutdown(context.Background()) }()
	}

	// M6: files janitor — prune expired tokens and orphan blobs.
	janitorInterval := time.Duration(cfg.Files.JanitorIntervalSeconds) * time.Second
	if janitorInterval == 0 {
		janitorInterval = 5 * time.Minute
	}
	blobGrace := time.Duration(cfg.Files.JanitorBlobGraceSeconds) * time.Second
	if blobGrace == 0 {
		blobGrace = 10 * time.Minute
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(janitorInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := filesSvc.Janitor(ctx, blobGrace); err != nil {
					logger.Warn("files janitor", "err", err)
				}
			}
		}
	}()

	// M6.6: security janitor — purge expired temporary ip_denials rows
	// from both the cache and the DB on a tunable interval.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(secSvc.EvictionInterval())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := secSvc.EvictExpired(ctx); err != nil {
					logger.Warn("security janitor", "err", err)
				}
			}
		}
	}()

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

// ipDenier is the minimal interface acceptLoop needs from the M6.6
// security service: a single accept-time check. Holding it as an
// interface keeps the loop test-friendly.
type ipDenier interface {
	IsIPDenied(netip.Addr) bool
}

func acceptLoop(ctx context.Context, ln net.Listener, h *session.Handler, logger *slog.Logger, wg *sync.WaitGroup, denier ipDenier) {
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
		if denier != nil {
			if addr := remoteIPFromConn(conn); addr.IsValid() && denier.IsIPDenied(addr) {
				logger.Info("connection denied",
					"session", conn.RemoteAddr().String(),
					"reason", "ip_denied")
				_ = conn.Close()
				continue
			}
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

// remoteIPFromConn extracts the netip.Addr of a TCP-or-TLS connection.
// Returns the zero Addr for non-TCP transports (test harnesses).
func remoteIPFromConn(c net.Conn) netip.Addr {
	tcp, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	addr, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// buildSecurityService constructs the M6.6 service from config. The
// optional UDP emitter is created here (and only here) so that startup
// fails fast on a misconfigured subtext-filter address.
func buildSecurityService(cfg config.SecurityConfig, db *store.DB, logger *slog.Logger) (*security.Service, error) {
	opts := security.Options{
		AutoDenyEnabled:         cfg.AutoDenyEnabled,
		AutoDenyTTL:             time.Duration(cfg.AutoDenyTTLSeconds) * time.Second,
		FailedPasswordThreshold: cfg.FailedPasswordThreshold,
		FailedPasswordWindow:    time.Duration(cfg.FailedPasswordWindowSeconds) * time.Second,
		EvictionInterval:        time.Duration(cfg.EvictionIntervalSeconds) * time.Second,
		Logger:                  logger,
	}
	if cfg.Filter.Enabled {
		em, err := security.NewEmitter(cfg.Filter.Address, logger)
		if err != nil {
			return nil, fmt.Errorf("filter: %w", err)
		}
		opts.Emitter = em
	}
	return security.New(db, opts), nil
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

// buildTerminalDeps constructs the TerminalDeps wired into every newly
// opened terminal engagement. The AccountFor closure looks up the
// player's body object in the world and resolves its account row. The
// rootCtx is the engine ctx so handler-initiated DB ops cancel cleanly
// on shutdown (mirrors NPCBinding.RootCtx).
func buildTerminalDeps(
	rootCtx context.Context,
	w *world.World,
	authStore *auth.Store,
	mailSvc *mail.Service,
	boardsSvc *boards.Service,
	filesSvc *files.Service,
	cfg *config.Config,
) *engage.TerminalDeps {
	accountFor := func(id world.ObjectID) (*auth.Account, error) {
		obj, err := w.Object(id)
		if err != nil {
			return nil, err
		}
		if obj.AccountID == nil {
			return nil, fmt.Errorf("object %d has no account", id)
		}
		return authStore.GetByID(rootCtx, *obj.AccountID)
	}
	base := fmt.Sprintf("https://%s", joinHostPort(cfg.Server.PublicHost, cfg.Server.HTTPPort))
	return &engage.TerminalDeps{
		RootCtx:     rootCtx,
		Mail:        mailSvc,
		Boards:      boardsSvc,
		Files:       filesSvc,
		UploadURL:   func(token string) string { return base + "/upload/" + token },
		DownloadURL: func(token string) string { return base + "/download/" + token },
		AccountFor:  accountFor,
		AccountByID: func(id int64) (*auth.Account, error) {
			return authStore.GetByID(rootCtx, id)
		},
	}
}

// terminalDepsWithSecurity adds the M6.6 admin-menu hooks to a base
// TerminalDeps. Called after the existing buildTerminalDeps so the
// callers in main.go only have to thread the extra deps once.
func terminalDepsWithSecurity(deps *engage.TerminalDeps, secSvc *security.Service, adminAPI *worldapi.API) *engage.TerminalDeps {
	deps.Security = secSvc
	deps.RenameAccount = adminAPI.RenameAccount
	return deps
}

// uploadMailNotifier returns the OnUpload callback wired into the M6
// HTTP handler. Delivery runs on context.Background rather than the
// process lifecycle ctx so that an upload finishing during the
// http.Server.Shutdown drain window — when the lifecycle ctx is already
// cancelled — still sends its mail (the DB writer is kept alive by
// `wg` until after the HTTP server exits).
func uploadMailNotifier(
	logger *slog.Logger,
	authStore *auth.Store,
	mailSvc *mail.Service,
) func(wintermutehttp.UploadEvent) {
	return func(ev wintermutehttp.UploadEvent) {
		logger.Info("file uploaded",
			"account_id", ev.AccountID, "file_id", ev.FileID,
			"slug", ev.Slug, "size", ev.Size, "mime", ev.MIME)
		ctx := context.Background()
		owner, err := authStore.GetByID(ctx, ev.AccountID)
		if err != nil {
			logger.Warn("upload mail: resolve owner",
				"account_id", ev.AccountID, "err", err)
			return
		}
		subject := "Upload complete: " + ev.Slug
		body := fmt.Sprintf(
			"Your upload completed.\n\n"+
				"  slug: %s\n"+
				"  size: %s (%d bytes)\n"+
				"  mime: %s\n\n"+
				"From a terminal in-world, type `download %s` to retrieve it.\n",
			ev.Slug, humanBytes(ev.Size), ev.Size, ev.MIME, ev.Slug)
		if _, err := mailSvc.SendFromSystem(ctx, owner.Username, subject, body); err != nil {
			logger.Warn("upload mail: send",
				"username", owner.Username, "slug", ev.Slug, "err", err)
		}
	}
}

// humanBytes renders n bytes as a short human-readable size (e.g.
// "12 B", "1.4 KB", "3.2 MB"). Used in upload-complete mail bodies.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffix := "KMGTPE"[exp]
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), suffix)
}

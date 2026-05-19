// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package menu

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// auditSink captures audit-log lines so tests can assert on them.
type auditSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	logr *slog.Logger
}

func newAuditSink() *auditSink {
	a := &auditSink{}
	a.logr = slog.New(slog.NewTextHandler(&a.buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return a
}

func (a *auditSink) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buf.String()
}

// adminFixture wires up an in-memory test world with one admin and one
// player, plus the service-layer deps the menu's admin feature needs.
type adminFixture struct {
	db     *store.DB
	auth   *auth.Store
	world  *world.World
	mail   *mail.Service
	boards *boards.Service
	files  *files.Service
	audit  *auditSink
	admin  *auth.Account
	player *auth.Account
}

func setupAdmin(t *testing.T) *adminFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin_menu.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := ftnnetworks.Bootstrap(ctx, db, nil, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	as := auth.NewStore(db)
	admin, err := as.Create(ctx, "root", "rootpass", auth.AccessAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	player, err := as.Create(ctx, "alice", "alicepass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("create player: %v", err)
	}
	w, err := world.Load(ctx, db, logger)
	if err != nil {
		t.Fatalf("world.Load: %v", err)
	}
	issuer := msgid.NewIssuer(db)
	mailSvc := mail.NewService(db, issuer, "Wintermute/test", "")
	boardsSvc := boards.NewService(db, issuer, boards.ServiceOptions{
		PID: "Wintermute/test", ServerName: "Wintermute",
	})
	filesSvc, err := files.NewService(db, filepath.Join(t.TempDir(), "files"))
	if err != nil {
		t.Fatalf("files.NewService: %v", err)
	}
	return &adminFixture{
		db: db, auth: as, world: w, mail: mailSvc, boards: boardsSvc,
		files: filesSvc, audit: newAuditSink(),
		admin: admin, player: player,
	}
}

func (f *adminFixture) deps(motd *string) *engage.TerminalDeps {
	get := func() string {
		if motd == nil {
			return ""
		}
		return *motd
	}
	set := func(_ context.Context, m string) error {
		if motd != nil {
			*motd = m
		}
		return nil
	}
	return &engage.TerminalDeps{
		RootCtx: context.Background(),
		Mail:    f.mail, Boards: f.boards, Files: f.files,
		Auth: f.auth, World: f.world, DB: f.db,
		AccountFor: func(_ world.ObjectID) (*auth.Account, error) {
			return f.admin, nil
		},
		AccountByID: func(id int64) (*auth.Account, error) {
			return f.auth.GetByID(context.Background(), id)
		},
		Logger:    f.audit.logr,
		GetMOTD:   get,
		SetMOTD:   set,
		StartedAt: time.Now().Add(-2 * time.Hour),
	}
}

func newAdminHandler(t *testing.T, f *adminFixture, motd *string) *Handler {
	t.Helper()
	h := NewHandler(&engage.Host{
		ObjectID: 100, Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{{Feature: engage.FeatureAdmin}},
	}, nil)
	h.SetDeps(f.deps(motd))
	return h
}

// enterAdmin walks an admin participant from the main menu into the admin
// console. Returns the participant and the output buffer ready for the
// next assertion. The buffer is reset so each test reads exactly what
// the action under test wrote.
func enterAdmin(t *testing.T, h *Handler) (*engage.Participant, *strings.Builder) {
	t.Helper()
	var buf strings.Builder
	p := newMenuParticipant(&buf)
	h.OnOpen(p)
	buf.Reset()
	h.Handle(p, "1") // the admin entry is the only one in the host's menu
	return p, &buf
}

// --- Top-level admin console -------------------------------------------

func TestAdmin_topLevel_listsAllSections(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	_, buf := enterAdmin(t, h)
	out := buf.String()
	for _, want := range []string{
		"Users", "Mail", "Boards", "Files",
		"Objects", "Rooms", "FTN networks", "System",
		"Back", "Quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("admin console missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestAdmin_back_returnsToMainMenu(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	buf.Reset()
	h.Handle(p, "B")
	if !strings.Contains(buf.String(), "Quit") || strings.Contains(buf.String(), "admin console") {
		t.Errorf("Back from admin should return to main menu: %s", buf.String())
	}
}

// --- Users -------------------------------------------------------------

func TestAdmin_users_listShowsAccounts(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	buf.Reset()
	h.Handle(p, "1") // Users
	out := buf.String()
	for _, want := range []string{"alice", "root", "player", "admin"} {
		if !strings.Contains(out, want) {
			t.Errorf("Users list missing %q\n%s", want, out)
		}
	}
}

func TestAdmin_users_promote_changesAccessLevel(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	h.Handle(p, "1") // Users
	// Find alice's row number. Accounts are sorted by username:
	// alice (1), root (2). Pick alice = 1.
	buf.Reset()
	h.Handle(p, "1") // alice's row
	if !strings.Contains(buf.String(), "alice") {
		t.Fatalf("did not land on alice's view: %s", buf.String())
	}
	buf.Reset()
	// User view: 1) Set access level, 2) Reset password. Open the
	// access-level submenu.
	h.Handle(p, "1")
	if !strings.Contains(buf.String(), "Set access level") {
		t.Fatalf("did not land on Set access level submenu: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "[current]") {
		t.Errorf("submenu missing [current] marker: %s", buf.String())
	}
	// Submenu: 1) Player [current], 2) Builder, 3) Admin. Pick 2.
	h.Handle(p, "2")
	got, err := f.auth.GetByID(context.Background(), f.player.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AccessLevel != auth.AccessBuilder {
		t.Errorf("AccessLevel after promote = %q, want builder", got.AccessLevel)
	}
	if !strings.Contains(f.audit.String(), "action=set_access_level") {
		t.Errorf("audit log missing set_access_level: %s", f.audit.String())
	}
	if !strings.Contains(f.audit.String(), "target=alice") {
		t.Errorf("audit log missing target=alice: %s", f.audit.String())
	}
}

func TestAdmin_users_setAccessLevel_sameLevelNoOp(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, _ := enterAdmin(t, h)
	h.Handle(p, "1") // Users
	h.Handle(p, "1") // alice (player)
	h.Handle(p, "1") // Set access level
	// Picking the level the target already holds must not log an audit
	// or change the row.
	h.Handle(p, "1") // Player [current]
	if strings.Contains(f.audit.String(), "action=set_access_level") {
		t.Errorf("no-op pick wrote an audit row: %s", f.audit.String())
	}
	got, _ := f.auth.GetByID(context.Background(), f.player.ID)
	if got.AccessLevel != auth.AccessPlayer {
		t.Errorf("AccessLevel = %q, want player", got.AccessLevel)
	}
}

func TestAdmin_users_selfPromote_refused(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	h.Handle(p, "1") // Users
	// root is the second account alphabetically (alice < root).
	h.Handle(p, "2") // root's row
	if !strings.Contains(buf.String(), "root") {
		t.Fatalf("did not land on root's view: %s", buf.String())
	}
	buf.Reset()
	// User view: 1) Set access level (refused on self).
	h.Handle(p, "1")
	got, err := f.auth.GetByID(context.Background(), f.admin.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AccessLevel != auth.AccessAdmin {
		t.Errorf("admin self-demoted: AccessLevel = %q, want admin", got.AccessLevel)
	}
	if !strings.Contains(buf.String(), "Cannot change your own access level") {
		t.Errorf("expected self-change refusal message: %s", buf.String())
	}
}

func TestAdmin_users_resetPassword_issuesTokenAndMail(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	h.Handle(p, "1") // Users
	buf.Reset()
	h.Handle(p, "1") // alice
	if !strings.Contains(buf.String(), "Reset password") {
		t.Fatalf("user view missing Reset password action: %s", buf.String())
	}
	buf.Reset()
	// User view: 1) Set access level, 2) Reset password. Pick 2.
	h.Handle(p, "2")
	out := buf.String()
	if !strings.Contains(out, "Password reset issued for alice") {
		t.Errorf("reveal screen missing summary: %s", out)
	}
	// The reveal screen claims mail was sent (mailSent=true on this
	// fixture, which wires a real Mail service).
	if !strings.Contains(out, "A notification mail has also been sent") {
		t.Errorf("reveal screen missing mail-sent confirmation: %s", out)
	}
	// The token is four hyphen-joined lowercase words; we don't pin the
	// value, just the shape. Capture it for the token-leak check below.
	tokenRe := regexp.MustCompile(`[a-z]+-[a-z]+-[a-z]+-[a-z]+`)
	token := tokenRe.FindString(out)
	if token == "" {
		t.Errorf("reveal screen missing four-word token: %s", out)
	}
	// Audit logged.
	if !strings.Contains(f.audit.String(), "action=password_reset_issued") {
		t.Errorf("audit log missing password_reset_issued: %s", f.audit.String())
	}
	if !strings.Contains(f.audit.String(), "target=alice") {
		t.Errorf("audit log missing target=alice: %s", f.audit.String())
	}
	// Alice received a system mail; the token must NOT appear in the body —
	// neither as the full hyphenated string nor as any of its constituent
	// words. The per-word check guards against a future refactor that
	// might happen to break apart the token in the mail body.
	box, err := f.mail.Inbox(context.Background(), f.player.ID)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(box) != 1 {
		t.Fatalf("len(inbox) = %d, want 1", len(box))
	}
	msg, err := f.mail.Read(context.Background(), box[0].ID, f.player.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(msg.Subject, "Password reset by root") {
		t.Errorf("subject = %q, want it to mention root", msg.Subject)
	}
	if strings.Contains(msg.Body, token) {
		t.Errorf("mail body leaked full token %q: %s", token, msg.Body)
	}
	for _, word := range strings.Split(token, "-") {
		if strings.Contains(msg.Body, word) {
			t.Errorf("mail body leaked token word %q: %s", word, msg.Body)
		}
	}
}

func TestAdmin_users_resetPassword_selfRefused(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	h.Handle(p, "1") // Users
	h.Handle(p, "2") // root
	buf.Reset()
	// User view: 1) Set access level, 2) Reset password. Pick 2 against
	// self — should be refused.
	h.Handle(p, "2")
	if !strings.Contains(buf.String(), "Cannot reset your own password") {
		t.Errorf("expected self-reset refusal: %s", buf.String())
	}
	got, _ := f.auth.GetByID(context.Background(), f.admin.ID)
	// Confirm no reset row was written for root.
	var resetHash sql.NullString
	if err := f.db.Read().QueryRowContext(context.Background(),
		`SELECT reset_hash FROM accounts WHERE id = ?`, got.ID).Scan(&resetHash); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if resetHash.Valid {
		t.Errorf("self-reset wrote a reset_hash; expected NULL")
	}
}

// --- Mail --------------------------------------------------------------

func TestAdmin_mail_broadcast_deliversToAllAccounts(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	h.Handle(p, "2") // Mail
	h.Handle(p, "1") // Broadcast
	h.Handle(p, "Notice")
	h.Handle(p, "body line one")
	h.Handle(p, "body line two")
	buf.Reset()
	h.Handle(p, ".") // commit
	// Both alice and root should have received it.
	box, _ := f.mail.Inbox(context.Background(), f.player.ID)
	if len(box) != 1 || box[0].Subject != "Notice" {
		t.Errorf("alice inbox = %+v, want 1 msg with subject 'Notice'", box)
	}
	box, _ = f.mail.Inbox(context.Background(), f.admin.ID)
	if len(box) != 1 || box[0].Subject != "Notice" {
		t.Errorf("root inbox = %+v, want 1 msg with subject 'Notice'", box)
	}
	if !strings.Contains(f.audit.String(), "action=broadcast_mail") {
		t.Errorf("audit log missing broadcast_mail: %s", f.audit.String())
	}
}

// --- Boards ------------------------------------------------------------

func TestAdmin_boards_singleNetwork_skipsPrompt(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	buf.Reset()
	h.Handle(p, "3") // Boards
	out := buf.String()
	// Only "local" exists, so we should land directly on the board list
	// for "local" (no "pick a network" header).
	if strings.Contains(out, "pick a network") {
		t.Errorf("single-network skip failed; got prompt:\n%s", out)
	}
	if !strings.Contains(out, "admin — Boards — local") {
		t.Errorf("expected board list for 'local' network: %s", out)
	}
}

func TestAdmin_boards_create_persistsBoard(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	h.Handle(p, "3") // Boards (lands on local's list)
	h.Handle(p, "C") // Create
	h.Handle(p, "general")
	h.Handle(p, "General Chat")
	h.Handle(p, "Casual conversation.")
	buf.Reset()
	h.Handle(p, ".") // commit description
	b, err := f.boards.GetBoard(context.Background(), "general")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if b.Name != "General Chat" {
		t.Errorf("board name = %q, want General Chat", b.Name)
	}
	if !strings.Contains(f.audit.String(), "action=create_board") {
		t.Errorf("audit log missing create_board: %s", f.audit.String())
	}
}

func TestAdmin_boards_setACL_updatesLevel(t *testing.T) {
	f := setupAdmin(t)
	_, err := f.boards.CreateBoard(context.Background(), boards.CreateBoardSpec{
		Slug: "general", Name: "General", NetworkSlug: "local",
	})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	h := newAdminHandler(t, f, nil)
	p, _ := enterAdmin(t, h)
	h.Handle(p, "3") // Boards (lands on local list)
	h.Handle(p, "1") // first board
	h.Handle(p, "P") // Set post level
	h.Handle(p, "200")
	got, _ := f.boards.GetBoard(context.Background(), "general")
	if got.PostMinLevel != 200 {
		t.Errorf("PostMinLevel = %d, want 200", got.PostMinLevel)
	}
}

// --- Objects -----------------------------------------------------------

func TestAdmin_objects_listShowsSeedObjects(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, buf := enterAdmin(t, h)
	buf.Reset()
	h.Handle(p, "5") // Objects
	out := buf.String()
	for _, slug := range []string{"coffee-cup", "datapad", "keycard"} {
		if !strings.Contains(out, slug) {
			t.Errorf("objects list missing %q\n%s", slug, out)
		}
	}
}

// --- Rooms -------------------------------------------------------------

func TestAdmin_rooms_editDescription_persists(t *testing.T) {
	f := setupAdmin(t)
	h := newAdminHandler(t, f, nil)
	p, _ := enterAdmin(t, h)
	h.Handle(p, "6") // Rooms
	// Rooms by slug: corridor (1), lobby (2), server-room (3). Pick lobby.
	h.Handle(p, "2")
	h.Handle(p, "E") // Edit description
	h.Handle(p, "Sparse and freshly painted.")
	h.Handle(p, ".") // commit
	r, _ := f.world.RoomBySlug("lobby")
	if !strings.Contains(r.Description, "Sparse and freshly painted") {
		t.Errorf("new description not persisted: %q", r.Description)
	}
	if !strings.Contains(f.audit.String(), "action=set_room_description") {
		t.Errorf("audit log missing set_room_description: %s", f.audit.String())
	}
}

// --- FTN networks ------------------------------------------------------

func TestAdmin_ftn_setDefault_movesFlag(t *testing.T) {
	f := setupAdmin(t)
	// Add a second network via direct DB write so we have something to
	// switch to. Use the same Bootstrap path the production code uses.
	if err := ftnnetworks.Bootstrap(context.Background(), f.db, []config.FTNNetwork{
		{Slug: "fsx", Name: "fsxNet", Domain: "fsxnet", Addr: "21:1/100.0"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	h := newAdminHandler(t, f, nil)
	p, _ := enterAdmin(t, h)
	h.Handle(p, "7") // FTN networks
	// Networks listed in id order: local (1), fsx (2). Local is default.
	h.Handle(p, "2") // pick fsx
	h.Handle(p, "S") // Set as default
	def, err := ftnnetworks.Default(context.Background(), f.db)
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if def.Slug != "fsx" {
		t.Errorf("default network = %q, want fsx", def.Slug)
	}
}

// --- System / MOTD -----------------------------------------------------

func TestAdmin_system_setMOTD_persists(t *testing.T) {
	f := setupAdmin(t)
	motd := ""
	h := newAdminHandler(t, f, &motd)
	p, _ := enterAdmin(t, h)
	h.Handle(p, "8") // System
	h.Handle(p, "E") // Edit MOTD
	h.Handle(p, "Welcome to the Sprawl.")
	h.Handle(p, ".")
	if motd != "Welcome to the Sprawl." {
		t.Errorf("MOTD = %q, want %q", motd, "Welcome to the Sprawl.")
	}
	if !strings.Contains(f.audit.String(), "action=set_motd") {
		t.Errorf("audit log missing set_motd: %s", f.audit.String())
	}
}

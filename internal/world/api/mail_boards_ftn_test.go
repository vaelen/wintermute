// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/config"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

// servicesEnv bundles the wired-up worldapi.API plus the underlying
// services and a couple of seeded accounts used by the M6.1 binding
// tests.
type servicesEnv struct {
	api   *API
	db    *store.DB
	mail  *mail.Service
	alice *auth.Account
	bob   *auth.Account
	admin *auth.Account
}

// newTestAPIWithServices is the M6.1 equivalent of newTestAPI but wires
// mail + boards services and seeds a couple of accounts plus a second
// FTN network so set_default has something to switch to.
func newTestAPIWithServices(t *testing.T) *servicesEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	db, err := store.Open(ctx, path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	nets := []config.FTNNetwork{
		{Slug: "fidonet", Name: "FidoNet", Domain: "fidonet", Addr: "1:1/100.0"},
		{Slug: "fsxnet", Name: "fsxNet", Domain: "fsxnet", Addr: "21:1/100.0"},
	}
	if err := ftnnetworks.Bootstrap(ctx, db, nets, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	w, err := world.Load(ctx, db, logger)
	if err != nil {
		t.Fatalf("world.Load: %v", err)
	}

	authStore := auth.NewStore(db)
	alice, err := authStore.Create(ctx, "alice", "alicepass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := authStore.Create(ctx, "bob", "bobpass", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	admin, err := authStore.Create(ctx, "admin", "adminpass", auth.AccessAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	issuer := msgid.NewIssuer(db)
	mailSvc := mail.NewService(db, issuer, "Wintermute/test", "")
	boardsSvc := boards.NewService(db, issuer, boards.ServiceOptions{
		PID:        "Wintermute/test",
		ServerName: "Wintermute",
		Tearline:   "--- Wintermute/test",
	})

	api := New(w, db, authStore, nil, logger)
	api.Mail = mailSvc
	api.Boards = boardsSvc

	return &servicesEnv{
		api: api, db: db, mail: mailSvc,
		alice: alice, bob: bob, admin: admin,
	}
}

// ---------------------------------------------------------------------------
// mail bindings

func TestSendMailFromSystem_DeliversWithSystemHandle(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	id, err := e.api.SendMailFromSystem(ctx, "bob", "Welcome", "Body.")
	if err != nil {
		t.Fatalf("SendMailFromSystem: %v", err)
	}
	m, err := e.mail.Read(ctx, id, e.bob.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.FromID.Valid {
		t.Errorf("FromID = %v, want NULL", m.FromID)
	}
	if m.FromName != mail.DefaultSystemName {
		t.Errorf("FromName = %q, want %q", m.FromName, mail.DefaultSystemName)
	}
}

func TestSendMailFromSystem_UnknownRecipient(t *testing.T) {
	e := newTestAPIWithServices(t)
	_, err := e.api.SendMailFromSystem(context.Background(), "nobody", "x", "y")
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %v, want *Error", err, err)
	}
	if apiErr.Code != CodeNotFound {
		t.Errorf("code = %q, want not_found", apiErr.Code)
	}
}

func TestBroadcastMail_DeliversToEveryAccount(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	n, err := e.api.BroadcastMail(ctx, "Welcome", "Body.", BroadcastMailOpts{})
	if err != nil {
		t.Fatalf("BroadcastMail: %v", err)
	}
	if n != 3 {
		t.Errorf("delivered = %d, want 3 (alice + bob + admin)", n)
	}
	// Every account's inbox grew by one with from_name = "<system>".
	for _, acc := range []*auth.Account{e.alice, e.bob, e.admin} {
		inbox, err := e.mail.Inbox(ctx, acc.ID)
		if err != nil {
			t.Fatalf("Inbox %s: %v", acc.Username, err)
		}
		if len(inbox) != 1 {
			t.Errorf("%s: inbox len = %d, want 1", acc.Username, len(inbox))
			continue
		}
		m := inbox[0]
		if m.FromID.Valid {
			t.Errorf("%s: FromID = %v, want NULL", acc.Username, m.FromID)
		}
		if m.FromName != mail.DefaultSystemName {
			t.Errorf("%s: FromName = %q, want %q",
				acc.Username, m.FromName, mail.DefaultSystemName)
		}
		if m.OriginAddr != "255:255/255.0@local" {
			t.Errorf("%s: OriginAddr = %q, want local", acc.Username, m.OriginAddr)
		}
	}
}

func TestBroadcastMail_FilterByAccessLevel(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	n, err := e.api.BroadcastMail(ctx, "Players Only", "x",
		BroadcastMailOpts{AccessLevel: string(auth.AccessPlayer)})
	if err != nil {
		t.Fatalf("BroadcastMail: %v", err)
	}
	if n != 2 {
		t.Errorf("delivered = %d, want 2 (only the player-tier accounts)", n)
	}
	adminInbox, _ := e.mail.Inbox(ctx, e.admin.ID)
	if len(adminInbox) != 0 {
		t.Errorf("admin inbox len = %d, want 0", len(adminInbox))
	}
}

func TestBroadcastMail_BogusLevelRejected(t *testing.T) {
	e := newTestAPIWithServices(t)
	_, err := e.api.BroadcastMail(context.Background(), "x", "y",
		BroadcastMailOpts{AccessLevel: "wizard"})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidArgument {
		t.Errorf("err = %v, want invalid_argument", err)
	}
}

func TestBroadcastMail_EmptySubjectOrBodyRejected(t *testing.T) {
	e := newTestAPIWithServices(t)
	_, err := e.api.BroadcastMail(context.Background(), "", "body", BroadcastMailOpts{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidArgument {
		t.Errorf("empty subject err = %v", err)
	}
	_, err = e.api.BroadcastMail(context.Background(), "subj", "", BroadcastMailOpts{})
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidArgument {
		t.Errorf("empty body err = %v", err)
	}
}

func TestUnreadMailCount_BeforeAndAfterRead(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	if _, err := e.api.SendMailFromSystem(ctx, "bob", "hi", "body"); err != nil {
		t.Fatalf("SendMailFromSystem: %v", err)
	}
	n, err := e.api.UnreadMailCount(ctx, "bob")
	if err != nil {
		t.Fatalf("UnreadMailCount: %v", err)
	}
	if n != 1 {
		t.Errorf("UnreadMailCount = %d, want 1", n)
	}
	box, _ := e.mail.Inbox(ctx, e.bob.ID)
	_, _ = e.mail.Read(ctx, box[0].ID, e.bob.ID)
	n, _ = e.api.UnreadMailCount(ctx, "bob")
	if n != 0 {
		t.Errorf("UnreadMailCount after read = %d, want 0", n)
	}
}

func TestUnreadMailCount_UnknownUser(t *testing.T) {
	e := newTestAPIWithServices(t)
	_, err := e.api.UnreadMailCount(context.Background(), "nobody")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestDeleteMailForUser_RemovesRow(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	id, err := e.api.SendMailFromSystem(ctx, "bob", "Bye", "x")
	if err != nil {
		t.Fatalf("SendMailFromSystem: %v", err)
	}
	if err := e.api.DeleteMailForUser(ctx, "bob", id); err != nil {
		t.Fatalf("DeleteMailForUser: %v", err)
	}
	box, _ := e.mail.Inbox(ctx, e.bob.ID)
	if len(box) != 0 {
		t.Errorf("bob inbox after delete len = %d, want 0", len(box))
	}
}

func TestDeleteMailForUser_NotFound(t *testing.T) {
	e := newTestAPIWithServices(t)
	err := e.api.DeleteMailForUser(context.Background(), "bob", 9999)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("err = %v, want not_found", err)
	}
}

// ---------------------------------------------------------------------------
// board bindings

func TestCreateBoard_LocalDefault(t *testing.T) {
	e := newTestAPIWithServices(t)
	id, err := e.api.CreateBoard(context.Background(), BoardSpec{
		Slug: "general", Name: "General Chat",
	})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if id == 0 {
		t.Errorf("id 0")
	}
	b, err := e.api.GetBoard(context.Background(), "general")
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	// Defaults: ReadMinLevel=Player, PostMinLevel=Player, AdminMinLevel=Admin.
	if b.ReadMinLevel != boards.LevelPlayer {
		t.Errorf("ReadMinLevel = %d, want %d", b.ReadMinLevel, boards.LevelPlayer)
	}
	if b.PostMinLevel != boards.LevelPlayer {
		t.Errorf("PostMinLevel = %d, want %d", b.PostMinLevel, boards.LevelPlayer)
	}
	if b.AdminMinLevel != boards.LevelAdmin {
		t.Errorf("AdminMinLevel = %d, want %d", b.AdminMinLevel, boards.LevelAdmin)
	}
}

func TestCreateBoard_OnFTNNetwork(t *testing.T) {
	e := newTestAPIWithServices(t)
	id, err := e.api.CreateBoard(context.Background(), BoardSpec{
		Slug: "fido-general", Name: "Fido General",
		NetworkSlug: "fidonet", AreaTag: "GENERAL",
	})
	if err != nil {
		t.Fatalf("CreateBoard fidonet: %v", err)
	}
	if id == 0 {
		t.Errorf("id 0")
	}
	b, _ := e.api.GetBoard(context.Background(), "fido-general")
	if !b.AreaTag.Valid || b.AreaTag.String != "GENERAL" {
		t.Errorf("AreaTag = %v, want GENERAL", b.AreaTag)
	}
	fidonet, _ := e.api.GetNetwork(context.Background(), "fidonet")
	if b.NetworkID != fidonet.ID {
		t.Errorf("NetworkID = %d, want %d", b.NetworkID, fidonet.ID)
	}
}

func TestCreateBoard_UnknownNetwork(t *testing.T) {
	e := newTestAPIWithServices(t)
	_, err := e.api.CreateBoard(context.Background(), BoardSpec{
		Slug: "x", Name: "X", NetworkSlug: "nosuch",
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidArgument {
		t.Errorf("err = %v, want invalid_argument", err)
	}
}

func TestCreateBoard_DuplicateSlug(t *testing.T) {
	e := newTestAPIWithServices(t)
	if _, err := e.api.CreateBoard(context.Background(), BoardSpec{
		Slug: "x", Name: "X",
	}); err != nil {
		t.Fatalf("first CreateBoard: %v", err)
	}
	_, err := e.api.CreateBoard(context.Background(), BoardSpec{
		Slug: "x", Name: "X2",
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeDuplicateSlug {
		t.Errorf("err = %v, want duplicate_slug", err)
	}
}

func TestDeleteBoard_NotFound(t *testing.T) {
	e := newTestAPIWithServices(t)
	err := e.api.DeleteBoard(context.Background(), "ghost")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestListAllBoards_ReturnsEvery(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	for _, spec := range []BoardSpec{
		{Slug: "general", Name: "Local"},
		{Slug: "fido-general", Name: "Fido", NetworkSlug: "fidonet"},
		{Slug: "fsxnet-general", Name: "Fsx", NetworkSlug: "fsxnet"},
	} {
		if _, err := e.api.CreateBoard(ctx, spec); err != nil {
			t.Fatalf("CreateBoard %s: %v", spec.Slug, err)
		}
	}
	out, err := e.api.ListAllBoards(ctx)
	if err != nil {
		t.Fatalf("ListAllBoards: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("got %d, want 3", len(out))
	}
	seen := map[string]bool{}
	for _, b := range out {
		seen[b.Slug] = true
	}
	for _, s := range []string{"general", "fido-general", "fsxnet-general"} {
		if !seen[s] {
			t.Errorf("missing board %q in list", s)
		}
	}
}

func TestSetBoardACLs_UpdatesOnlySupplied(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	if _, err := e.api.CreateBoard(ctx, BoardSpec{Slug: "x", Name: "X"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	postBuilder := boards.LevelBuilder
	if err := e.api.SetBoardACLs(ctx, "x", BoardACLs{PostMinLevel: &postBuilder}); err != nil {
		t.Fatalf("SetBoardACLs: %v", err)
	}
	b, _ := e.api.GetBoard(ctx, "x")
	if b.PostMinLevel != boards.LevelBuilder {
		t.Errorf("PostMinLevel = %d, want %d", b.PostMinLevel, boards.LevelBuilder)
	}
	// Other levels untouched.
	if b.ReadMinLevel != boards.LevelPlayer {
		t.Errorf("ReadMinLevel changed unexpectedly: %d", b.ReadMinLevel)
	}
}

func TestSetBoardACLs_NoFields(t *testing.T) {
	e := newTestAPIWithServices(t)
	if _, err := e.api.CreateBoard(context.Background(),
		BoardSpec{Slug: "x", Name: "X"}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	err := e.api.SetBoardACLs(context.Background(), "x", BoardACLs{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidArgument {
		t.Errorf("err = %v, want invalid_argument", err)
	}
}

func TestSetBoardACLs_NotFound(t *testing.T) {
	e := newTestAPIWithServices(t)
	lvl := boards.LevelPlayer
	err := e.api.SetBoardACLs(context.Background(), "ghost",
		BoardACLs{ReadMinLevel: &lvl})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestGetBoard_NotFound(t *testing.T) {
	e := newTestAPIWithServices(t)
	_, err := e.api.GetBoard(context.Background(), "ghost")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("err = %v, want not_found", err)
	}
}

// ---------------------------------------------------------------------------
// ftn network bindings

func TestListNetworks_IncludesLocalAndDeclared(t *testing.T) {
	e := newTestAPIWithServices(t)
	rows, err := e.api.ListNetworks(context.Background())
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	seen := map[string]bool{}
	var defaultSlug string
	for _, n := range rows {
		seen[n.Slug] = true
		if n.IsDefault {
			defaultSlug = n.Slug
		}
	}
	for _, s := range []string{"local", "fidonet", "fsxnet"} {
		if !seen[s] {
			t.Errorf("missing network %q", s)
		}
	}
	// Bootstrap with no explicit default → local is the default.
	if defaultSlug != "local" {
		t.Errorf("default = %q, want local", defaultSlug)
	}
}

func TestGetNetwork_NotFound(t *testing.T) {
	e := newTestAPIWithServices(t)
	_, err := e.api.GetNetwork(context.Background(), "ghost")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("err = %v, want not_found", err)
	}
}

func TestSetDefaultNetwork_SwitchesAndKeepsIndexValid(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	if err := e.api.SetDefaultNetwork(ctx, "fidonet"); err != nil {
		t.Fatalf("SetDefaultNetwork: %v", err)
	}
	rows, err := e.api.ListNetworks(ctx)
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	defaults := 0
	var defaultSlug string
	for _, n := range rows {
		if n.IsDefault {
			defaults++
			defaultSlug = n.Slug
		}
	}
	if defaults != 1 {
		t.Errorf("default count = %d, want 1 (partial unique index)", defaults)
	}
	if defaultSlug != "fidonet" {
		t.Errorf("default = %q, want fidonet", defaultSlug)
	}
}

func TestSetDefaultNetwork_Unknown(t *testing.T) {
	e := newTestAPIWithServices(t)
	ctx := context.Background()
	err := e.api.SetDefaultNetwork(ctx, "ghost")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("err = %v, want not_found", err)
	}
	// The transaction must roll back: the existing default network
	// remains. After bootstrap with no explicit default, `local` is the
	// default and clearing it would leave no default at all.
	rows, listErr := e.api.ListNetworks(ctx)
	if listErr != nil {
		t.Fatalf("ListNetworks: %v", listErr)
	}
	defaults := 0
	var defaultSlug string
	for _, n := range rows {
		if n.IsDefault {
			defaults++
			defaultSlug = n.Slug
		}
	}
	if defaults != 1 {
		t.Errorf("default count = %d, want 1 (rollback failed)", defaults)
	}
	if defaultSlug != "local" {
		t.Errorf("default = %q, want local (rollback failed)", defaultSlug)
	}
}

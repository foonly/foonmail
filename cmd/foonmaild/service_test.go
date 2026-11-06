package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/dbustest"
	"foonmail/internal/imapsync"
	"foonmail/internal/rpc"
)

type fakeBackend struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (b *fakeBackend) record(s string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, s)
	return b.err
}

func (b *fakeBackend) setErr(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

func (b *fakeBackend) recorded() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

func (b *fakeBackend) SyncFolder(_ context.Context, folderID int64) error {
	return b.record("sync")
}

func (b *fakeBackend) FetchBody(_ context.Context, folderID int64, uid uint32) (int64, error) {
	if err := b.record("fetch"); err != nil {
		return 0, err
	}
	return 1000 + int64(uid), nil
}

func (b *fakeBackend) DeleteMessages(_ context.Context, folderID int64, uids []uint32) error {
	return b.record("delete")
}

func (b *fakeBackend) MoveMessages(_ context.Context, folderID int64, uids []uint32, dest int64) error {
	return b.record("move")
}

func (b *fakeBackend) AppendDraft(_ context.Context, raw []byte) error {
	return b.record("draft")
}

func (b *fakeBackend) CreateFolder(_ context.Context, name string) (int64, error) {
	return 77, b.record("create " + name)
}

func (b *fakeBackend) DeleteFolder(_ context.Context, folderID int64) error {
	return b.record(fmt.Sprint("delete folder ", folderID))
}

func (b *fakeBackend) SetFlags(_ context.Context, folderID int64, uids []uint32, add, remove []string) error {
	if !slices.Equal(uids, []uint32{3, 4}) || !slices.Equal(add, []string{`\Seen`}) || len(remove) != 0 {
		return errors.New("unexpected SetFlags arguments")
	}
	return b.record("flags")
}

type serviceEnv struct {
	t       *testing.T
	client  *rpc.Client
	backend *fakeBackend
	outbox  *fakeOutbox
	hub     *hub
	inbox   int64
	bus     *dbustest.Bus
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	bus := dbustest.Start(t)
	db, err := cache.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	details := true
	cfg := &config.Config{Accounts: []config.Account{{Name: "home", Email: "me@example.org", NotifyDetails: &details}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := newHub(ctx, cfg, db, nil, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	h.delay = 10 * time.Millisecond
	acct, _ := db.EnsureAccount(ctx, "home")
	b := &fakeBackend{}
	fo := &fakeOutbox{}
	svc, err := exportService(bus.Conn(), db, map[int64]backend{acct: b}, fo, h, log)
	if err != nil {
		t.Fatal(err)
	}
	h.signals = svc
	go h.run(ctx)
	return &serviceEnv{t: t, client: rpc.NewClient(bus.Conn()), backend: b, outbox: fo, hub: h,
		inbox: h.accounts["home"].inbox, bus: bus}
}

func callCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestServiceMethods(t *testing.T) {
	e := newServiceEnv(t)
	ctx := callCtx(t)

	id, err := e.client.FetchBody(ctx, e.inbox, 7)
	if err != nil || id != 1007 {
		t.Errorf("FetchBody = %d, %v", id, err)
	}
	if err := e.client.SyncFolder(ctx, e.inbox); err != nil {
		t.Errorf("SyncFolder: %v", err)
	}
	if err := e.client.SetFlags(ctx, e.inbox, []uint32{3, 4}, []string{`\Seen`}, nil); err != nil {
		t.Errorf("SetFlags: %v", err)
	}
	if !slices.Equal(e.backend.recorded(), []string{"fetch", "sync", "flags"}) {
		t.Errorf("backend calls = %v", e.backend.recorded())
	}

	// Errors keep their meaning across the bus.
	if _, err := e.client.FetchBody(ctx, 9999, 1); !errors.Is(err, rpc.ErrNotFound) {
		t.Errorf("unknown folder: err = %v", err)
	}
	e.backend.setErr(errors.Join(imapsync.ErrOffline, errors.New("dial tcp: no route to host")))
	err = e.client.SyncFolder(ctx, e.inbox)
	if !errors.Is(err, rpc.ErrOffline) || !errorsContains(err, "no route to host") {
		t.Errorf("offline: err = %v", err)
	}
}

func errorsContains(err error, s string) bool {
	return err != nil && strings.Contains(err.Error(), s)
}

func TestServiceStatusAndSignals(t *testing.T) {
	e := newServiceEnv(t)
	ctx := callCtx(t)
	events, stop, err := e.client.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	waitFor := func(what string, match func(rpc.Event) bool) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case ev := <-events:
				if match(ev) {
					return
				}
			case <-deadline:
				t.Fatalf("no %s signal", what)
			}
		}
	}

	e.hub.notify(imapsync.ConnectionChanged{Account: "home", Online: true})
	e.hub.notify(imapsync.UnreadChanged{Account: "home", Unread: 404})
	waitFor("StatusChanged", func(ev rpc.Event) bool { _, ok := ev.(rpc.StatusChanged); return ok })
	st, err := e.client.Status(ctx)
	if err != nil || len(st) != 1 || st[0] != (rpc.AccountStatus{Name: "home", Online: true, Unread: 404}) {
		t.Fatalf("Status = %+v, %v", st, err)
	}

	e.hub.notify(imapsync.NewMail{Account: "home", UID: 5, Envelope: cache.Envelope{From: cache.Address{Addr: "bob@example.com"}}})
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, _ = e.client.Status(ctx)
		if st[0].New == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st[0].New != 1 {
		t.Fatalf("new mail not in Status: %+v", st)
	}
	if err := e.client.Acknowledge(ctx); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		st, _ = e.client.Status(ctx)
		if st[0].New == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st[0].New != 0 {
		t.Errorf("Acknowledge didn't clear new mail: %+v", st)
	}

	e.hub.notify(imapsync.FolderChanged{Account: "home", FolderID: e.inbox})
	waitFor("FolderChanged", func(ev rpc.Event) bool {
		fc, ok := ev.(rpc.FolderChanged)
		return ok && fc.FolderID == e.inbox
	})
}

func TestServiceNotRunningAndSecondDaemon(t *testing.T) {
	bus := dbustest.Start(t)
	c := rpc.NewClient(bus.Conn())
	if _, err := c.Status(callCtx(t)); !errors.Is(err, rpc.ErrNotRunning) {
		t.Errorf("no daemon: err = %v", err)
	}

	e := newServiceEnv(t)
	if _, err := exportService(e.bus.Conn(), nil, nil, nil, nil, slog.Default()); err == nil {
		t.Error("a second service claimed the bus name")
	}
}

func TestServiceDrafts(t *testing.T) {
	e := newServiceEnv(t)
	ctx := callCtx(t)
	if err := e.client.SaveDraft(ctx, "home", []byte("To: bob@example.com\r\n\r\nx")); err != nil {
		t.Fatal(err)
	}
	if err := e.client.DeleteMessages(ctx, e.inbox, []uint32{4}); err != nil {
		t.Fatal(err)
	}
	if err := e.client.MoveMessages(ctx, e.inbox, []uint32{4}, 99); err != nil {
		t.Fatal(err)
	}
	if got := e.backend.recorded(); !slices.Equal(got, []string{"draft", "delete", "move"}) {
		t.Errorf("backend calls = %v", got)
	}
	if err := e.client.SaveDraft(ctx, "nobody", []byte("x")); !errors.Is(err, rpc.ErrNotFound) {
		t.Errorf("unknown account: err = %v", err)
	}
}

func TestServiceFolders(t *testing.T) {
	e := newServiceEnv(t)
	ctx := callCtx(t)
	id, err := e.client.CreateFolder(ctx, "home", "Clients/Acme")
	if err != nil || id != 77 {
		t.Errorf("CreateFolder = %d, %v", id, err)
	}
	if err := e.client.DeleteFolder(ctx, e.inbox); err != nil {
		t.Errorf("DeleteFolder: %v", err)
	}
	want := []string{"create Clients/Acme", fmt.Sprint("delete folder ", e.inbox)}
	if !slices.Equal(e.backend.recorded(), want) {
		t.Errorf("backend calls = %v", e.backend.recorded())
	}
	if _, err := e.client.CreateFolder(ctx, "nobody", "X"); !errors.Is(err, rpc.ErrNotFound) {
		t.Errorf("unknown account: err = %v", err)
	}
}

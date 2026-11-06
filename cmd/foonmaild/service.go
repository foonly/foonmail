package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"foonmail/internal/cache"
	"foonmail/internal/imapsync"
	"foonmail/internal/outbox"
	"foonmail/internal/rpc"
)

const callTimeout = time.Minute

// backend is what the service needs from an account's imapsync.Syncer.
type backend interface {
	SyncFolder(ctx context.Context, folderID int64) error
	FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error)
	SetFlags(ctx context.Context, folderID int64, uids []uint32, add, remove []string) error
	DeleteMessages(ctx context.Context, folderID int64, uids []uint32) error
	MoveMessages(ctx context.Context, folderID int64, uids []uint32, dest int64) error
	AppendDraft(ctx context.Context, raw []byte) error
	CreateFolder(ctx context.Context, name string) (int64, error)
	DeleteFolder(ctx context.Context, folderID int64) error
}

// service exports rpc.Interface on the session bus.
type service struct {
	conn     *dbus.Conn
	db       *cache.DB
	backends map[int64]backend // by cache account id
	hub      *hub
	outbox   outboxAPI // nil when the outbox couldn't be opened
	log      *slog.Logger
}

// exportService puts the daemon's interface on conn and claims rpc.BusName.
func exportService(conn *dbus.Conn, db *cache.DB, backends map[int64]backend, ob outboxAPI, h *hub, log *slog.Logger) (*service, error) {
	s := &service{conn: conn, db: db, backends: backends, outbox: ob, hub: h, log: log}
	if err := conn.Export(methods{s}, rpc.Path, rpc.Interface); err != nil {
		return nil, err
	}
	node := &introspect.Node{
		Name: string(rpc.Path),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{Name: rpc.Interface, Methods: introspect.Methods(methods{}), Signals: []introspect.Signal{
				{Name: "FolderChanged", Args: []introspect.Arg{{Name: "folder", Type: "x"}}},
				{Name: "StatusChanged"},
				{Name: "OutboxChanged"},
			}},
		},
	}
	if err := conn.Export(introspect.NewIntrospectable(node), rpc.Path, "org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, err
	}
	reply, err := conn.RequestName(rpc.BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("another foonmaild already owns %s on this session bus", rpc.BusName)
	}
	return s, nil
}

// FolderChanged and StatusChanged emit the signals; the hub calls them.
func (s *service) FolderChanged(folderID int64) {
	if err := s.conn.Emit(rpc.Path, rpc.SignalFolderChanged, folderID); err != nil {
		s.log.Warn("emit FolderChanged", "err", err)
	}
}

func (s *service) StatusChanged() {
	if err := s.conn.Emit(rpc.Path, rpc.SignalStatusChanged); err != nil {
		s.log.Warn("emit StatusChanged", "err", err)
	}
}

func (s *service) OutboxChanged() {
	if err := s.conn.Emit(rpc.Path, rpc.SignalOutboxChanged); err != nil {
		s.log.Warn("emit OutboxChanged", "err", err)
	}
}

func (s *service) backendFor(ctx context.Context, folderID int64) (backend, error) {
	f, err := s.db.FolderByID(ctx, folderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("folder %d: %w", folderID, imapsync.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	b := s.backends[f.AccountID]
	if b == nil {
		return nil, fmt.Errorf("folder %d belongs to no configured account: %w", folderID, imapsync.ErrNotFound)
	}
	return b, nil
}

// methods holds the exported D-Bus methods, so service's own methods aren't.
type methods struct{ s *service }

func (m methods) FetchBody(folderID int64, uid uint32) (int64, *dbus.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	b, err := m.s.backendFor(ctx, folderID)
	if err != nil {
		return 0, dbusError(err)
	}
	id, err := b.FetchBody(ctx, folderID, uid)
	return id, dbusError(err)
}

func (m methods) SyncFolder(folderID int64) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	b, err := m.s.backendFor(ctx, folderID)
	if err != nil {
		return dbusError(err)
	}
	return dbusError(b.SyncFolder(ctx, folderID))
}

func (m methods) SetFlags(folderID int64, uids []uint32, add, remove []string) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	b, err := m.s.backendFor(ctx, folderID)
	if err != nil {
		return dbusError(err)
	}
	return dbusError(b.SetFlags(ctx, folderID, uids, add, remove))
}

func (m methods) Acknowledge() *dbus.Error {
	m.s.hub.acknowledge()
	return nil
}

func (m methods) Status() ([]rpc.AccountStatus, *dbus.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := m.s.hub.status(ctx)
	return st, dbusError(err)
}

// dbusError maps sync errors to the interface's error names.
func dbusError(err error) *dbus.Error {
	if err == nil {
		return nil
	}
	name := rpc.ErrNameFailed
	switch {
	case errors.Is(err, imapsync.ErrOffline):
		name = rpc.ErrNameOffline
	case errors.Is(err, imapsync.ErrNotFound):
		name = rpc.ErrNameNotFound
	case errors.Is(err, outbox.ErrNotFound):
		name = rpc.ErrNameNotFound
	case errors.Is(err, outbox.ErrTooLate):
		name = rpc.ErrNameTooLate
	case errors.Is(err, outbox.ErrTooLarge):
		name = rpc.ErrNameTooLarge
	}
	return &dbus.Error{Name: name, Body: []any{err.Error()}}
}

// outboxAPI is what the service needs from the outbox.
type outboxAPI interface {
	Submit(ctx context.Context, account string, raw []byte) (int64, error)
	Undo(ctx context.Context, id int64) error
	Resolve(ctx context.Context, id int64, resend bool) error
	List(ctx context.Context) ([]outbox.Item, error)
}

var errNoOutbox = errors.New("the outbox isn't available; see foonmaild's log")

func (m methods) Send(account string, raw []byte) (int64, *dbus.Error) {
	if m.s.outbox == nil {
		return 0, dbusError(errNoOutbox)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	id, err := m.s.outbox.Submit(ctx, account, raw)
	return id, dbusError(err)
}

func (m methods) Undo(id int64) *dbus.Error {
	if m.s.outbox == nil {
		return dbusError(errNoOutbox)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return dbusError(m.s.outbox.Undo(ctx, id))
}

func (m methods) Resolve(id int64, resend bool) *dbus.Error {
	if m.s.outbox == nil {
		return dbusError(errNoOutbox)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return dbusError(m.s.outbox.Resolve(ctx, id, resend))
}

func (m methods) Outbox() ([]rpc.OutboxItem, *dbus.Error) {
	if m.s.outbox == nil {
		return nil, dbusError(errNoOutbox)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	items, err := m.s.outbox.List(ctx)
	if err != nil {
		return nil, dbusError(err)
	}
	out := make([]rpc.OutboxItem, len(items))
	for i, it := range items {
		out[i] = rpc.OutboxItem{ID: it.ID, Account: it.Account, State: string(it.State),
			Subject: it.Subject, To: it.To, Error: it.Error, Created: it.Created.Unix()}
	}
	return out, nil
}

func (m methods) DeleteMessages(folderID int64, uids []uint32) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	b, err := m.s.backendFor(ctx, folderID)
	if err != nil {
		return dbusError(err)
	}
	return dbusError(b.DeleteMessages(ctx, folderID, uids))
}

func (m methods) SaveDraft(account string, raw []byte) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	id, err := m.s.db.AccountID(ctx, account)
	if err != nil {
		return dbusError(fmt.Errorf("account %s: %w", account, imapsync.ErrNotFound))
	}
	b := m.s.backends[id]
	if b == nil {
		return dbusError(fmt.Errorf("account %s isn't configured: %w", account, imapsync.ErrNotFound))
	}
	return dbusError(b.AppendDraft(ctx, raw))
}

func (m methods) MoveMessages(folderID int64, uids []uint32, dest int64) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	b, err := m.s.backendFor(ctx, folderID)
	if err != nil {
		return dbusError(err)
	}
	return dbusError(b.MoveMessages(ctx, folderID, uids, dest))
}

// CreateFolder makes a folder in an account; the name has "/" between
// levels. It returns the new folder's id.
func (m methods) CreateFolder(account, name string) (int64, *dbus.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	id, err := m.s.db.AccountID(ctx, account)
	if err != nil {
		return 0, dbusError(fmt.Errorf("account %s: %w", account, imapsync.ErrNotFound))
	}
	b := m.s.backends[id]
	if b == nil {
		return 0, dbusError(fmt.Errorf("account %s isn't configured: %w", account, imapsync.ErrNotFound))
	}
	folderID, err := b.CreateFolder(ctx, name)
	return folderID, dbusError(err)
}

// DeleteFolder deletes a folder and the mail in it.
func (m methods) DeleteFolder(folderID int64) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	b, err := m.s.backendFor(ctx, folderID)
	if err != nil {
		return dbusError(err)
	}
	return dbusError(b.DeleteFolder(ctx, folderID))
}

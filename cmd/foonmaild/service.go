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
	"foonmail/internal/rpc"
)

const callTimeout = time.Minute

// backend is what the service needs from an account's imapsync.Syncer.
type backend interface {
	SyncFolder(ctx context.Context, folderID int64) error
	FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error)
	SetFlags(ctx context.Context, folderID int64, uids []uint32, add, remove []string) error
}

// service exports rpc.Interface on the session bus.
type service struct {
	conn     *dbus.Conn
	db       *cache.DB
	backends map[int64]backend // by cache account id
	hub      *hub
	log      *slog.Logger
}

// exportService puts the daemon's interface on conn and claims rpc.BusName.
func exportService(conn *dbus.Conn, db *cache.DB, backends map[int64]backend, h *hub, log *slog.Logger) (*service, error) {
	s := &service{conn: conn, db: db, backends: backends, hub: h, log: log}
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
	m.s.hub.activate()
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
	}
	return &dbus.Error{Name: name, Body: []any{err.Error()}}
}

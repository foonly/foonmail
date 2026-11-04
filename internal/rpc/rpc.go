// Package rpc is the D-Bus interface between foonmaild and the GUI.
//
// The GUI reads the cache directly (read-only) and uses this interface only
// for what needs the daemon:
//
//	FetchBody(folder x, uid u) → (message x)   download a message body into the cache
//	SyncFolder(folder x)                        sync a folder now
//	SetFlags(folder x, uids au, add as, remove as)
//	Acknowledge()                               the user is looking: clear "new"
//	Status() → (a(sbsii))                       per account: name, online, error, unread, new
//
// and two signals:
//
//	FolderChanged(folder x)   the cache's copy of a folder changed
//	StatusChanged()           call Status again
//
// Folders are cache folder ids; messages are cache message ids.
package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	BusName   = "fi.uplink.Foonmail"
	Path      = dbus.ObjectPath("/fi/uplink/Foonmail")
	Interface = "fi.uplink.Foonmail1"

	SignalFolderChanged = Interface + ".FolderChanged"
	SignalStatusChanged = Interface + ".StatusChanged"

	// D-Bus error names, mapped to the Go errors below.
	ErrNameOffline  = Interface + ".Error.Offline"
	ErrNameNotFound = Interface + ".Error.NotFound"
	ErrNameFailed   = Interface + ".Error.Failed"
)

var (
	// ErrNotRunning means foonmaild isn't on the bus.
	ErrNotRunning = errors.New("foonmaild is not running")
	// ErrOffline means the account is offline; cached data is still readable.
	ErrOffline = errors.New("account is offline")
	// ErrNotFound means the folder or message doesn't exist.
	ErrNotFound = errors.New("not found")
)

// AccountStatus is one account's state.
type AccountStatus struct {
	Name   string
	Online bool
	Error  string // why it is offline, if it is
	Unread int32  // in INBOX
	New    int32  // new since the user last looked
}

// Client calls foonmaild.
type Client struct {
	conn *dbus.Conn
	obj  dbus.BusObject
}

// NewClient returns a client using conn, normally the session bus.
func NewClient(conn *dbus.Conn) *Client {
	return &Client{conn: conn, obj: conn.Object(BusName, Path)}
}

func (c *Client) call(ctx context.Context, method string, args ...any) *dbus.Call {
	return c.obj.CallWithContext(ctx, Interface+"."+method, 0, args...)
}

// FetchBody makes sure a message's raw content is in the cache and returns
// its message id.
func (c *Client) FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error) {
	var id int64
	err := c.call(ctx, "FetchBody", folderID, uid).Store(&id)
	return id, mapError(err)
}

// SyncFolder syncs a folder now.
func (c *Client) SyncFolder(ctx context.Context, folderID int64) error {
	return mapError(c.call(ctx, "SyncFolder", folderID).Err)
}

// SetFlags adds and removes flags on messages in a folder.
func (c *Client) SetFlags(ctx context.Context, folderID int64, uids []uint32, add, remove []string) error {
	if add == nil {
		add = []string{}
	}
	if remove == nil {
		remove = []string{}
	}
	return mapError(c.call(ctx, "SetFlags", folderID, uids, add, remove).Err)
}

// Acknowledge tells the daemon the user is looking at their mail.
func (c *Client) Acknowledge(ctx context.Context) error {
	return mapError(c.call(ctx, "Acknowledge").Err)
}

// Status returns every account's state.
func (c *Client) Status(ctx context.Context) ([]AccountStatus, error) {
	var st []AccountStatus
	err := c.call(ctx, "Status").Store(&st)
	return st, mapError(err)
}

// Event is a signal from the daemon: FolderChanged or StatusChanged.
type Event interface{ isEvent() }

// FolderChanged means a folder's list should be reloaded.
type FolderChanged struct{ FolderID int64 }

// StatusChanged means Status should be called again.
type StatusChanged struct{}

func (FolderChanged) isEvent() {}
func (StatusChanged) isEvent() {}

// Subscribe delivers the daemon's signals until stop is called.
func (c *Client) Subscribe() (events <-chan Event, stop func(), err error) {
	opts := []dbus.MatchOption{dbus.WithMatchInterface(Interface), dbus.WithMatchObjectPath(Path)}
	if err := c.conn.AddMatchSignal(opts...); err != nil {
		return nil, nil, err
	}
	sigs := make(chan *dbus.Signal, 32)
	out := make(chan Event, 32)
	c.conn.Signal(sigs)
	go func() {
		defer close(out)
		for s := range sigs {
			switch s.Name {
			case SignalFolderChanged:
				if len(s.Body) == 1 {
					if id, ok := s.Body[0].(int64); ok {
						out <- FolderChanged{FolderID: id}
					}
				}
			case SignalStatusChanged:
				out <- StatusChanged{}
			}
		}
	}()
	return out, func() {
		c.conn.RemoveSignal(sigs)
		c.conn.RemoveMatchSignal(opts...)
		close(sigs)
	}, nil
}

func mapError(err error) error {
	var de dbus.Error
	if !errors.As(err, &de) {
		var dep *dbus.Error
		if !errors.As(err, &dep) {
			return err
		}
		de = *dep
	}
	msg := ""
	if len(de.Body) > 0 {
		msg, _ = de.Body[0].(string)
	}
	wrap := func(sentinel error) error {
		if msg == "" {
			return sentinel
		}
		return fmt.Errorf("%w: %s", sentinel, msg)
	}
	switch de.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
		return ErrNotRunning
	case ErrNameOffline:
		return wrap(ErrOffline)
	case ErrNameNotFound:
		return wrap(ErrNotFound)
	}
	return err
}

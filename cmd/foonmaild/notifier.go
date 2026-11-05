package main

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"foonmail/internal/notify"
)

var errNoNotificationServer = errors.New("no notification server")

// lazyNotifier connects to the notification server when first needed and
// tries again later if it isn't there, since the daemon may start before
// the desktop's notification server does.
type lazyNotifier struct {
	conn *dbus.Conn
	log  *slog.Logger

	mu     sync.Mutex
	client *notify.Client
	retry  time.Time
	warned bool
}

func (l *lazyNotifier) get() *notify.Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil || time.Now().Before(l.retry) {
		return l.client
	}
	c, err := notify.New(l.conn)
	if err != nil {
		l.retry = time.Now().Add(10 * time.Second)
		if !l.warned {
			l.log.Warn("no notification server yet; will try again when needed", "err", err)
			l.warned = true
		}
		return nil
	}
	srv := c.Server()
	l.log.Info("notification server", "name", srv.Name, "version", srv.Version,
		"actions", srv.Has("actions"), "capabilities", srv.Capabilities)
	if !srv.Has("actions") {
		l.log.Warn("the notification server has no action buttons: clicking notifications " +
			"won't open mail, and undo send needs foonmailctl or the GUI")
	}
	l.client = c
	return c
}

func (l *lazyNotifier) Send(n *notify.Notification) (uint32, error) {
	c := l.get()
	if c == nil {
		return 0, errNoNotificationServer
	}
	return c.Send(n)
}

func (l *lazyNotifier) Server() notify.Server {
	if c := l.get(); c != nil {
		return c.Server()
	}
	return notify.Server{}
}

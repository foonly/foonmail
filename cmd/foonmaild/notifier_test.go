package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/godbus/dbus/v5"

	"foonmail/internal/dbustest"
	"foonmail/internal/notify"
)

type miniServer struct{}

func (miniServer) GetCapabilities() ([]string, *dbus.Error) { return []string{"actions"}, nil }
func (miniServer) GetServerInformation() (string, string, string, string, *dbus.Error) {
	return "mini", "test", "1", "1.2", nil
}
func (miniServer) Notify(string, uint32, string, string, string, []string, map[string]dbus.Variant, int32) (uint32, *dbus.Error) {
	return 42, nil
}

func TestNotifierConnectsWhenTheServerAppears(t *testing.T) {
	bus := dbustest.Start(t)
	l := &lazyNotifier{conn: bus.Conn(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := l.Send(&notify.Notification{Summary: "early"}); !errors.Is(err, errNoNotificationServer) {
		t.Fatalf("before the server: err = %v", err)
	}

	srv := bus.Conn()
	srv.Export(miniServer{}, "/org/freedesktop/Notifications", "org.freedesktop.Notifications")
	if _, err := srv.RequestName("org.freedesktop.Notifications", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	// Retries are spaced out; a retry still in the future means no server.
	if _, err := l.Send(&notify.Notification{Summary: "too soon"}); !errors.Is(err, errNoNotificationServer) {
		t.Errorf("within the retry delay: err = %v", err)
	}
	l.mu.Lock()
	l.retry = l.retry.AddDate(-1, 0, 0)
	l.mu.Unlock()
	id, err := l.Send(&notify.Notification{Summary: "now"})
	if err != nil || id != 42 || !l.Server().Has("actions") {
		t.Errorf("after the server appeared: id %d, err %v, server %+v", id, err, l.Server())
	}
}

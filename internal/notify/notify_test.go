package notify

import (
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"foonmail/internal/dbustest"
)

// fakeServer implements org.freedesktop.Notifications on a private bus.
type fakeServer struct {
	conn *dbus.Conn
	caps []string

	mu   sync.Mutex
	next uint32
	got  []call
}

type call struct {
	app, icon, summary, body string
	replaces                 uint32
	actions                  []string
	hints                    map[string]dbus.Variant
	timeout                  int32
	closed                   uint32
}

func (s *fakeServer) GetCapabilities() ([]string, *dbus.Error) { return s.caps, nil }

func (s *fakeServer) GetServerInformation() (string, string, string, string, *dbus.Error) {
	return "fake", "foonmail tests", "1", "1.2", nil
}

func (s *fakeServer) Notify(app string, replaces uint32, icon, summary, body string,
	actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := replaces
	if id == 0 {
		s.next++
		id = s.next
	}
	s.got = append(s.got, call{app: app, icon: icon, summary: summary, body: body, replaces: replaces,
		actions: actions, hints: hints, timeout: timeout})
	return id, nil
}

func (s *fakeServer) CloseNotification(id uint32) *dbus.Error {
	s.mu.Lock()
	s.got = append(s.got, call{closed: id})
	s.mu.Unlock()
	if err := s.conn.Emit(objPath, iface+".NotificationClosed", id, ClosedByCall); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (s *fakeServer) calls() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call(nil), s.got...)
}

func startServer(t *testing.T, bus *dbustest.Bus, caps ...string) *fakeServer {
	t.Helper()
	conn := bus.Conn()
	s := &fakeServer{conn: conn, caps: caps}
	if err := conn.Export(s, objPath, iface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName: %v %v", reply, err)
	}
	return s
}

func TestSendAndActions(t *testing.T) {
	bus := dbustest.Start(t)
	srv := startServer(t, bus, "actions", "body-markup")
	c, err := New(bus.Conn())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Server().Has("actions") || c.Server().Name != "fake" {
		t.Errorf("server = %+v", c.Server())
	}

	actions := make(chan string, 1)
	closed := make(chan uint32, 1)
	id, err := c.Send(&Notification{
		AppName: "foonmail", Icon: "mail-unread", Summary: "Bob",
		Body:     "Re: <b>tags</b> & things",
		Actions:  []Action{{"default", "Open"}, {"undo", "Undo"}},
		Category: "email.arrived", Urgency: 1, Timeout: 10 * time.Second,
		OnAction: func(k string) { actions <- k },
		OnClosed: func(_, r uint32) { closed <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.calls()[0]
	if got.body != "Re: &lt;b&gt;tags&lt;/b&gt; &amp; things" {
		t.Errorf("body not escaped for a markup server: %q", got.body)
	}
	if len(got.actions) != 4 || got.actions[2] != "undo" || got.timeout != 10000 {
		t.Errorf("call = %+v", got)
	}
	if got.hints["category"].Value() != "email.arrived" || got.hints["urgency"].Value() != byte(1) {
		t.Errorf("hints = %v", got.hints)
	}

	// A click on another application's notification is ignored.
	srv.conn.Emit(objPath, iface+".ActionInvoked", id+100, "default")
	srv.conn.Emit(objPath, iface+".ActionInvoked", id, "undo")
	select {
	case k := <-actions:
		if k != "undo" {
			t.Errorf("action = %q", k)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ActionInvoked not delivered")
	}

	if err := c.Close(id); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-closed:
		if r != ClosedByCall {
			t.Errorf("close reason = %d", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NotificationClosed not delivered")
	}
}

func TestPlainServerGetsRawBodyAndReplace(t *testing.T) {
	bus := dbustest.Start(t)
	srv := startServer(t, bus) // no capabilities
	c, err := New(bus.Conn())
	if err != nil {
		t.Fatal(err)
	}
	if c.Server().Has("actions") {
		t.Error("server without actions reported as having them")
	}
	id, _ := c.Send(&Notification{Summary: "3 new messages", Body: "a < b", Timeout: -1})
	id2, _ := c.Send(&Notification{ReplacesID: id, Summary: "4 new messages"})
	calls := srv.calls()
	if calls[0].body != "a < b" || calls[0].timeout != 0 {
		t.Errorf("first call = %+v", calls[0])
	}
	if id2 != id || calls[1].replaces != id || calls[1].timeout != -1 {
		t.Errorf("replace: id %d -> %d, call %+v", id, id2, calls[1])
	}
}

func TestNoServer(t *testing.T) {
	bus := dbustest.Start(t)
	if _, err := New(bus.Conn()); err == nil {
		t.Error("New succeeded without a notification server")
	}
}

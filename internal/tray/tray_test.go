package tray

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"foonmail/internal/dbustest"
)

// fakeWatcher is a tray host's StatusNotifierWatcher.
type fakeWatcher struct {
	conn *dbus.Conn
	mu   sync.Mutex
	regs []string
}

func (w *fakeWatcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.mu.Lock()
	w.regs = append(w.regs, service)
	w.mu.Unlock()
	return nil
}

func (w *fakeWatcher) registrations() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.regs...)
}

func startWatcher(t *testing.T, bus *dbustest.Bus) *fakeWatcher {
	t.Helper()
	conn := bus.Conn()
	w := &fakeWatcher{conn: conn}
	if err := conn.Export(w, watcherPath, watcherName); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName: %v %v", reply, err)
	}
	return w
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRegisterPropertiesAndActivate(t *testing.T) {
	bus := dbustest.Start(t)
	w := startWatcher(t, bus)
	activated := make(chan struct{}, 1)
	tr, err := New(bus.Conn(), "foonmail",
		State{Status: Active, IconName: "mail-read", Tooltip: "foonmail\nuplink: 404 unread"},
		func() { activated <- struct{}{} }, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	eventually(t, "registration", func() bool { return len(w.registrations()) == 1 })
	name := w.registrations()[0]
	if !strings.HasPrefix(name, "org.kde.StatusNotifierItem-") {
		t.Errorf("registered %q", name)
	}

	// The host reads properties and calls methods on the registered name.
	host := bus.Conn()
	obj := host.Object(name, itemPath)
	get := func(p string) any {
		t.Helper()
		v, err := obj.GetProperty(itemIface + "." + p)
		if err != nil {
			t.Fatalf("get %s: %v", p, err)
		}
		return v.Value()
	}
	if get("Status") != "Active" || get("IconName") != "mail-read" || get("Id") != "foonmail" {
		t.Errorf("Status %v, IconName %v, Id %v", get("Status"), get("IconName"), get("Id"))
	}
	tip := get("ToolTip").([]any)
	if tip[2] != "foonmail" || tip[3] != "uplink: 404 unread" {
		t.Errorf("ToolTip = %v", tip)
	}

	if err := obj.Call(itemIface+".Activate", 0, int32(0), int32(0)).Err; err != nil {
		t.Fatal(err)
	}
	select {
	case <-activated:
	case <-time.After(2 * time.Second):
		t.Fatal("Activate did not reach the callback")
	}

	// Changes are signalled and visible as properties.
	if err := host.AddMatchSignal(dbus.WithMatchInterface(itemIface)); err != nil {
		t.Fatal(err)
	}
	sigs := make(chan *dbus.Signal, 10)
	host.Signal(sigs)
	tr.Set(State{Status: NeedsAttention, IconName: "mail-unread", Tooltip: "foonmail\n1 new"})
	want := map[string]bool{itemIface + ".NewStatus": true, itemIface + ".NewIcon": true, itemIface + ".NewToolTip": true}
	deadline := time.After(2 * time.Second)
	for len(want) > 0 {
		select {
		case s := <-sigs:
			delete(want, s.Name)
		case <-deadline:
			t.Fatalf("signals not received: %v", want)
		}
	}
	if get("Status") != "NeedsAttention" || get("IconName") != "mail-unread" {
		t.Errorf("after Set: Status %v, IconName %v", get("Status"), get("IconName"))
	}

	// Introspection works, for hosts that use it.
	var xml string
	if err := obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xml, `name="Activate"`) || !strings.Contains(xml, `name="IconName"`) {
		t.Errorf("introspection incomplete:\n%s", xml)
	}
}

func TestRegistersWhenHostStartsLaterAndRestarts(t *testing.T) {
	bus := dbustest.Start(t)
	tr, err := New(bus.Conn(), "foonmail", State{Status: Active, IconName: "mail-read"}, nil, quiet)
	if err != nil {
		t.Fatal(err) // no host yet is not an error
	}
	defer tr.Close()

	w := startWatcher(t, bus)
	eventually(t, "registration with a host that started later", func() bool { return len(w.registrations()) == 1 })

	// The host restarts, as Quickshell does when its config is reloaded.
	w.conn.Close()
	w2 := startWatcher(t, bus)
	eventually(t, "registration with the restarted host", func() bool { return len(w2.registrations()) == 1 })
}

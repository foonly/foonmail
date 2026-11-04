// Package dbustest starts a private D-Bus session bus for tests, so tests
// of D-Bus services never touch the real desktop session.
package dbustest

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// Bus is a private session bus.
type Bus struct {
	Address string
	t       *testing.T
}

// Start runs dbus-daemon for the duration of the test. It skips the test if
// dbus-daemon isn't installed.
func Start(t *testing.T) *Bus {
	t.Helper()
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(path, "--session", "--nofork", "--nopidfile", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("reading dbus-daemon address: %v", err)
	}
	return &Bus{Address: strings.TrimSpace(addr), t: t}
}

// Conn opens a new connection to the bus, closed at the end of the test.
func (b *Bus) Conn() *dbus.Conn {
	b.t.Helper()
	c, err := dbus.Connect(b.Address)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { c.Close() })
	return c
}

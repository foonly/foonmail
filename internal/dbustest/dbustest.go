// Package dbustest starts a private D-Bus session bus for tests, so tests
// of D-Bus services never touch the real desktop session.
package dbustest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
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
	// A minimal config of its own: the standard session config would pull in
	// the user's D-Bus service files, and a test asking for a name could
	// then start the user's real programs.
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=`+dir+`</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "--config-file="+conf, "--nofork", "--nopidfile", "--print-address=1")
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

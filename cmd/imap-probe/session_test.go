package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// syncBuffer collects the probe's output for the test to poll.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output so far:\n%s", want, s.String())
}

func startServer(t *testing.T) string {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("u", "p")
	_ = user.Create("INBOX", nil)
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       log.New(&strings.Builder{}, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// other is a second client acting like the phone.
func other(t *testing.T, addr string) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Login("u", "p").Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}

func appendMsg(t *testing.T, c *imapclient.Client, subject string) {
	t.Helper()
	msg := fmt.Sprintf("From: Bob <bob@example.com>\r\nSubject: %s\r\n\r\nHello\r\n", subject)
	cmd := c.Append("INBOX", int64(len(msg)), nil)
	if _, err := cmd.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func store(t *testing.T, c *imapclient.Client, uid imap.UID, flag imap.Flag) {
	t.Helper()
	err := c.Store(imap.UIDSetNum(uid), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{flag}}, nil).Close()
	if err != nil {
		t.Fatal(err)
	}
}

func expunge(t *testing.T, c *imapclient.Client, uid imap.UID) {
	t.Helper()
	store(t, c, uid, imap.FlagDeleted)
	if err := c.Expunge().Close(); err != nil {
		t.Fatal(err)
	}
}

func newTestProber(addr string, out *syncBuffer) *prober {
	return &prober{
		addr: addr, user: "u", pass: "p", mailbox: "INBOX",
		check: time.Hour, out: log.New(out, "", 0),
		dial: imapclient.DialInsecure,
	}
}

func runSession(p *prober, wake chan struct{}) (cancel func() error) {
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.session(ctx, wake) }()
	return func() error {
		stop()
		return <-done
	}
}

func TestLiveChangesDuringIdle(t *testing.T) {
	addr := startServer(t)
	phone := other(t, addr)
	appendMsg(t, phone, "first")
	appendMsg(t, phone, "second")

	var out syncBuffer
	p := newTestProber(addr, &out)
	stop := runSession(p, make(chan struct{}))
	out.waitFor(t, "loaded 2 messages, 2 unread")
	out.waitFor(t, "IDLE on INBOX")

	appendMsg(t, phone, "third")
	out.waitFor(t, `NEW    uid 3 from Bob <bob@example.com>: "third" [unread]`)

	store(t, phone, 1, imap.FlagSeen) // read on the phone
	out.waitFor(t, `FLAGS  uid 1: (none) -> \Seen`)

	expunge(t, phone, 2)
	out.waitFor(t, "GONE   uid 2 (expunged)")

	if err := stop(); err != context.Canceled {
		t.Errorf("session returned %v, want context.Canceled", err)
	}
	if got := fmt.Sprint(p.state.uids); got != "[1 3]" {
		t.Errorf("state uids = %s, want [1 3]", got)
	}
}

func TestResyncAfterDisconnect(t *testing.T) {
	addr := startServer(t)
	phone := other(t, addr)
	for _, s := range []string{"a", "b", "c"} {
		appendMsg(t, phone, s)
	}

	var out syncBuffer
	p := newTestProber(addr, &out)
	stop := runSession(p, make(chan struct{}))
	out.waitFor(t, "IDLE on INBOX")
	stop()

	// Changes while the probe is offline.
	expunge(t, phone, 1)
	store(t, phone, 3, imap.FlagSeen)
	appendMsg(t, phone, "d")

	stop = runSession(p, make(chan struct{}))
	defer stop()
	out.waitFor(t, "GONE   uid 1 (while disconnected)")
	out.waitFor(t, `FLAGS  uid 3: (none) -> \Seen (while disconnected)`)
	out.waitFor(t, `NEW    uid 4 from Bob <bob@example.com>: "d" [unread] (while disconnected)`)
	out.waitFor(t, "resync: 1 new, 1 gone, 1 flag changes")
}

func TestResumeFromSleepEndsSession(t *testing.T) {
	addr := startServer(t)
	var out syncBuffer
	p := newTestProber(addr, &out)
	wake := make(chan struct{}, 1)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- p.session(ctx, wake) }()
	out.waitFor(t, "IDLE on INBOX")
	wake <- struct{}{}
	select {
	case err := <-done:
		if err != errResumed {
			t.Errorf("session returned %v, want errResumed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end after a resume signal")
	}
}

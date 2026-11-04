package imapsync

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"foonmail/internal/cache"
	"foonmail/internal/config"
)

// env is a test server, a cache, a Syncer and a "phone" client that makes
// changes on the server the way another device would.
type env struct {
	t      *testing.T
	addr   string
	db     *cache.DB
	syncer *Syncer
	phone  *imapclient.Client
	waker  *Waker

	mu     sync.Mutex
	events []Event
	cancel context.CancelFunc
	done   chan struct{}
}

func newEnv(t *testing.T, folders ...string) *env {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("u", "p")
	for _, f := range append([]string{"INBOX"}, folders...) {
		_ = user.Create(f, nil)
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	db, err := cache.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	e := &env{t: t, addr: ln.Addr().String(), db: db, waker: &Waker{}}
	e.phone = e.client()
	e.syncer = e.newSyncer()
	return e
}

func (e *env) newSyncer() *Syncer {
	host, port, _ := net.SplitHostPort(e.addr)
	p, _ := strconv.Atoi(port)
	acct := &config.Account{
		Name:  "test",
		Email: "me@example.com",
		IMAP:  config.Server{Host: host, Port: p, Username: "u", Security: config.SecurityTLS},
		Folders: config.Folders{
			Watched:      []string{"Sent"},
			PollInterval: 50 * time.Millisecond,
		},
	}
	s, err := New(context.Background(), Options{
		Account:  acct,
		DB:       e.db,
		Log:      testLogger(),
		Password: func(config.Server) (string, error) { return "p", nil },
		Notify: func(ev Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
		Dial: func(srv config.Server, opts *imapclient.Options) (*imapclient.Client, error) {
			return imapclient.DialInsecure(srv.Address(), opts)
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) client() *imapclient.Client {
	e.t.Helper()
	c, err := imapclient.DialInsecure(e.addr, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { c.Close() })
	if err := c.Login("u", "p").Wait(); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan struct{})
	go func() {
		e.syncer.Run(ctx, e.waker.Subscribe(), e.waker.Subscribe())
		close(e.done)
	}()
}

func (e *env) stop() {
	e.cancel()
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		e.t.Fatal("Run did not return after cancel")
	}
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

func (e *env) folder(name string) (cache.Folder, bool) {
	fs, err := e.db.Folders(context.Background(), e.syncer.accountID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, f := range fs {
		if f.Name == name {
			return f, true
		}
	}
	return cache.Folder{}, false
}

func (e *env) uids(name string) []uint32 {
	f, ok := e.folder(name)
	if !ok {
		return nil
	}
	u, err := e.db.UIDs(context.Background(), f.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) newMail() []NewMail {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []NewMail
	for _, ev := range e.events {
		if n, ok := ev.(NewMail); ok {
			out = append(out, n)
		}
	}
	return out
}

func (e *env) lastUnread() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.events) - 1; i >= 0; i-- {
		if u, ok := e.events[i].(UnreadChanged); ok {
			return u.Unread
		}
	}
	return -1
}

// Phone actions.

func (e *env) appendTo(mailbox, subject string, headers ...string) {
	e.t.Helper()
	msg := fmt.Sprintf("From: Bob Example <bob@example.com>\r\nTo: me@example.com\r\nSubject: %s\r\n"+
		"Date: Wed, 04 Nov 2026 12:00:00 +0200\r\nMessage-ID: <%s@example.com>\r\n%s\r\nHello\r\n",
		subject, strings.ReplaceAll(subject, " ", "-"), strings.Join(headers, ""))
	cmd := e.phone.Append(mailbox, int64(len(msg)), nil)
	if _, err := cmd.Write([]byte(msg)); err != nil {
		e.t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		e.t.Fatal(err)
	}
	if _, err := cmd.Wait(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) phoneSelect(mailbox string) {
	e.t.Helper()
	if _, err := e.phone.Select(mailbox, nil).Wait(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) markSeen(mailbox string, uid uint32) {
	e.t.Helper()
	e.phoneSelect(mailbox)
	err := e.phone.Store(imap.UIDSetNum(imap.UID(uid)),
		&imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) delete(mailbox string, uid uint32) {
	e.t.Helper()
	e.phoneSelect(mailbox)
	err := e.phone.Store(imap.UIDSetNum(imap.UID(uid)),
		&imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.phone.Expunge().Close(); err != nil {
		e.t.Fatal(err)
	}
}

// Tests.

func TestInitialSyncThenLiveChanges(t *testing.T) {
	e := newEnv(t, "Sent")
	e.appendTo("INBOX", "first")
	e.appendTo("INBOX", "second reply",
		"In-Reply-To: <first@example.com>\r\n",
		"References: <root@example.com>\r\n <first@example.com>\r\n")
	e.appendTo("Sent", "my answer")

	e.start()
	defer e.stop()
	e.eventually("INBOX synced", func() bool { return slices.Equal(e.uids("INBOX"), []uint32{1, 2}) })
	e.eventually("Sent synced", func() bool { return len(e.uids("Sent")) == 1 })
	e.eventually("unread count", func() bool { return e.lastUnread() == 2 })
	if n := e.newMail(); len(n) != 0 {
		t.Errorf("first sync notified about %d messages; it must not", len(n))
	}

	// Envelope and threading headers made it into the cache.
	inbox, _ := e.folder("INBOX")
	list, err := e.db.List(context.Background(), inbox.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reply cache.Summary
	for _, s := range list {
		if s.UID == 2 {
			reply = s
		}
	}
	if reply.Subject != "second reply" || reply.From != (cache.Address{Name: "Bob Example", Addr: "bob@example.com"}) {
		t.Errorf("reply summary = %+v", reply)
	}
	h, err := e.db.Header(context.Background(), reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.References, []string{"root@example.com", "first@example.com"}) ||
		h.InReplyTo != "first@example.com" || h.MessageID != "second-reply@example.com" || h.To != "me@example.com" {
		t.Errorf("header = %+v", h)
	}

	// New mail while in IDLE.
	e.appendTo("INBOX", "third")
	e.eventually("new mail event", func() bool { return len(e.newMail()) == 1 })
	if n := e.newMail()[0]; n.Envelope.Subject != "third" || n.UID != 3 || n.Seen {
		t.Errorf("NewMail = %+v", n)
	}
	e.eventually("third in cache", func() bool { return slices.Equal(e.uids("INBOX"), []uint32{1, 2, 3}) })

	// Read on the phone.
	e.markSeen("INBOX", 1)
	e.eventually("flag change in cache", func() bool {
		flags, _ := e.db.Flags(context.Background(), inbox.ID)
		return slices.Contains(flags[1], `\Seen`)
	})
	e.eventually("unread count after read", func() bool { return e.lastUnread() == 2 })

	// Deleted on the phone.
	e.delete("INBOX", 2)
	e.eventually("expunge in cache", func() bool { return slices.Equal(e.uids("INBOX"), []uint32{1, 3}) })

	// A watched folder picks up changes by polling.
	e.appendTo("Sent", "another answer")
	e.eventually("Sent poll", func() bool { return len(e.uids("Sent")) == 2 })
	if len(e.newMail()) != 1 {
		t.Errorf("mail in Sent must not notify; got %d NewMail events", len(e.newMail()))
	}
}

func TestResyncAfterRestartNotifiesWhatArrived(t *testing.T) {
	e := newEnv(t, "Sent")
	e.appendTo("INBOX", "a")
	e.appendTo("INBOX", "b")
	e.appendTo("INBOX", "c")

	e.start()
	e.eventually("initial sync", func() bool { return len(e.uids("INBOX")) == 3 })
	e.stop()

	// While disconnected.
	e.delete("INBOX", 1)
	e.markSeen("INBOX", 3)
	e.appendTo("INBOX", "d")

	e.start() // same Syncer: this is a reconnect, not a fresh start
	defer e.stop()
	e.eventually("resync", func() bool { return slices.Equal(e.uids("INBOX"), []uint32{2, 3, 4}) })
	inbox, _ := e.folder("INBOX")
	flags, _ := e.db.Flags(context.Background(), inbox.ID)
	if !slices.Contains(flags[3], `\Seen`) {
		t.Errorf("flag change while away not applied: %v", flags)
	}
	e.eventually("NewMail for mail that arrived while away", func() bool {
		n := e.newMail()
		return len(n) == 1 && n[0].Envelope.Subject == "d"
	})
}

func TestFreshProcessDoesNotNotify(t *testing.T) {
	e := newEnv(t)
	e.appendTo("INBOX", "a")
	e.start()
	e.eventually("sync", func() bool { return len(e.uids("INBOX")) == 1 })
	e.stop()

	e.appendTo("INBOX", "b")
	e.syncer = e.newSyncer() // like restarting the daemon
	e.start()
	defer e.stop()
	e.eventually("sync after restart", func() bool { return len(e.uids("INBOX")) == 2 })
	if n := e.newMail(); len(n) != 0 {
		t.Errorf("first sync of a new process notified: %+v", n)
	}
}

func TestFolderRecreatedAndDeleted(t *testing.T) {
	e := newEnv(t, "Sent", "Old")
	e.appendTo("Sent", "x")
	e.start()
	defer e.stop()
	e.eventually("Sent synced", func() bool { return len(e.uids("Sent")) == 1 })
	before, _ := e.folder("Sent")

	// Recreating a folder gives it a new UIDVALIDITY: the old UIDs mean nothing.
	if err := e.phone.Delete("Sent").Wait(); err != nil {
		t.Fatal(err)
	}
	if err := e.phone.Create("Sent", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	e.appendTo("Sent", "y")
	e.eventually("Sent refetched under the new UIDVALIDITY", func() bool {
		f, ok := e.folder("Sent")
		return ok && f.UIDValidity != before.UIDValidity && slices.Equal(e.uids("Sent"), []uint32{1})
	})
	f, _ := e.folder("Sent")
	list, _ := e.db.List(context.Background(), f.ID, 10, 0)
	if len(list) != 1 || list[0].Subject != "y" {
		t.Errorf("Sent after recreate = %+v", list)
	}

	if err := e.phone.Delete("Old").Wait(); err != nil {
		t.Fatal(err)
	}
	e.eventually("deleted folder dropped from cache", func() bool { _, ok := e.folder("Old"); return !ok })
}

func TestResumeReconnects(t *testing.T) {
	e := newEnv(t)
	e.start()
	defer e.stop()
	connected := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()
		n := 0
		for _, ev := range e.events {
			if c, ok := ev.(ConnectionChanged); ok && c.Online {
				n++
			}
		}
		return n
	}
	e.eventually("first connection", func() bool { return connected() == 1 })
	e.waker.Wake()
	e.eventually("reconnected after resume", func() bool { return connected() == 2 })
}

func TestParseReferences(t *testing.T) {
	got := parseReferences([]byte("References: <a@x>\r\n\t<b@y> junk <c@z>\r\n\r\n"))
	if !slices.Equal(got, []string{"a@x", "b@y", "c@z"}) {
		t.Errorf("got %q", got)
	}
}

func TestDiffUIDs(t *testing.T) {
	added, removed := diffUIDs([]uint32{1, 2, 4, 7}, []uint32{2, 3, 4, 8, 9})
	if !slices.Equal(added, []uint32{3, 8, 9}) || !slices.Equal(removed, []uint32{1, 7}) {
		t.Errorf("added %v, removed %v", added, removed)
	}
}

// testLogger discards logs unless SYNC_TEST_LOG is set.
func testLogger() *slog.Logger {
	if os.Getenv("SYNC_TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNotifyOnlyAboveBaseline(t *testing.T) {
	var got []uint32
	s := &Syncer{acct: &config.Account{Name: "a"}, opts: Options{Notify: func(ev Event) {
		got = append(got, ev.(NewMail).UID)
	}}}
	s.setBaseline(&imap.SelectData{UIDValidity: 7, UIDNext: 10})
	s.setBaseline(&imap.SelectData{UIDValidity: 7, UIDNext: 15}) // a reconnect keeps the first baseline
	s.notifyNew([]cache.NewMessage{{UID: 8}, {UID: 9}, {UID: 10}, {UID: 12}})
	if !slices.Equal(got, []uint32{10, 12}) {
		t.Errorf("notified %v, want [10 12]", got)
	}

	got = nil
	s.setBaseline(&imap.SelectData{UIDValidity: 8, UIDNext: 3}) // UIDVALIDITY changed: new baseline
	s.notifyNew([]cache.NewMessage{{UID: 1}, {UID: 3}})
	if !slices.Equal(got, []uint32{3}) {
		t.Errorf("after UIDVALIDITY change notified %v, want [3]", got)
	}
}

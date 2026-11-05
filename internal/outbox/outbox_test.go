package outbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"foonmail/internal/config"
)

// server is a real SMTP server whose behaviour tests can change.
type server struct {
	addr string

	mu       sync.Mutex
	received []received
	rcptErr  *smtp.SMTPError // refuse RCPT TO
	dataErr  *smtp.SMTPError // refuse after DATA
	hangUp   bool            // take the message, then drop the connection without answering
	block    chan struct{}   // if set, DATA waits for it
	inData   chan struct{}   // closed when a DATA starts
}

type received struct {
	from  string
	rcpts []string
	data  string
}

func (s *server) all() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.received...)
}

type session struct {
	s     *server
	conn  *smtp.Conn
	from  string
	rcpts []string
}

func (ss *session) AuthMechanisms() []string { return []string{sasl.Plain} }
func (ss *session) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, user, pass string) error {
		if user != "me" || pass != "secret" {
			return errors.New("bad login")
		}
		return nil
	}), nil
}
func (ss *session) Reset()        { ss.from, ss.rcpts = "", nil }
func (ss *session) Logout() error { return nil }
func (ss *session) Mail(from string, _ *smtp.MailOptions) error {
	ss.from = from
	return nil
}
func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	ss.s.mu.Lock()
	err := ss.s.rcptErr
	ss.s.mu.Unlock()
	if err != nil {
		return err
	}
	ss.rcpts = append(ss.rcpts, to)
	return nil
}
func (ss *session) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	ss.s.mu.Lock()
	block, inData, dataErr, hangUp := ss.s.block, ss.s.inData, ss.s.dataErr, ss.s.hangUp
	if dataErr == nil {
		ss.s.received = append(ss.s.received, received{ss.from, ss.rcpts, string(b)})
	}
	ss.s.mu.Unlock()
	if inData != nil {
		close(inData)
	}
	if block != nil {
		<-block
	}
	if hangUp {
		ss.conn.Conn().Close()
		return errors.New("hung up")
	}
	if dataErr != nil {
		return dataErr
	}
	return nil
}

func startServer(t *testing.T, maxBytes int64) *server {
	t.Helper()
	s := &server{}
	srv := smtp.NewServer(smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		return &session{s: s, conn: c}, nil
	}))
	srv.AllowInsecureAuth = true
	srv.MaxMessageBytes = maxBytes
	srv.ErrorLog = log.New(io.Discard, "", 0)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	s.addr = ln.Addr().String()
	return s
}

type copier struct {
	mu     sync.Mutex
	sent   []string
	drafts []string
	err    error
}

func (c *copier) AppendSent(_ context.Context, _ string, raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.sent = append(c.sent, string(raw))
	return nil
}

func (c *copier) AppendDraft(_ context.Context, _ string, raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drafts = append(c.drafts, string(raw))
	return nil
}

func (c *copier) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent), len(c.drafts)
}

type env struct {
	t      *testing.T
	srv    *server
	ob     *Outbox
	copier *copier
	dir    string

	mu     sync.Mutex
	events []Event
	cancel context.CancelFunc
	done   chan struct{}
}

func account() config.Account {
	yes := true
	return config.Account{
		Name: "home", Email: "me@example.org", DisplayName: "Kim", Aliases: []string{"postmaster@example.org"},
		AppendSent: &yes,
		SMTP:       config.Server{Host: "127.0.0.1", Username: "me"},
	}
}

func newEnv(t *testing.T, maxBytes int64) *env {
	t.Helper()
	e := &env{t: t, srv: startServer(t, maxBytes), copier: &copier{}, dir: t.TempDir()}
	e.open()
	return e
}

func (e *env) open() {
	sender := &SMTP{
		Password: func(config.Server) (string, error) { return "secret", nil },
		Dial: func(ctx context.Context, _ config.Server) (*smtp.Client, error) {
			return smtp.Dial(e.srv.addr)
		},
		Timeout: 3 * time.Second,
	}
	ob, err := Open(Options{
		DataDir:  e.dir,
		Accounts: []config.Account{account()},
		Sender:   sender,
		Copier:   e.copier,
		Delay:    100 * time.Millisecond,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Notify: func(ev Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	sender.OnSizeLimit = ob.RememberSizeLimit
	e.ob = ob
	e.t.Cleanup(func() { ob.Close() })
}

func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan struct{})
	go func() { e.ob.Run(ctx); close(e.done) }()
	e.t.Cleanup(e.stop)
}

func (e *env) stop() {
	if e.cancel != nil {
		e.cancel()
		<-e.done
		e.cancel = nil
	}
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

func (e *env) hasEvent(kind EventKind, id int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Kind == kind && ev.ID == id {
			return true
		}
	}
	return false
}

func (e *env) state(id int64) State {
	it, err := e.ob.store.get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return it.State
}

const msg = "From: Kim <me@example.org>\nTo: Bob <bob@example.com>\nCc: liisa@example.com\n" +
	"Bcc: secret@example.com\nSubject: Kahvi?\n\nHei!\n"

func (e *env) submit(raw string) int64 {
	e.t.Helper()
	id, err := e.ob.Submit(context.Background(), "home", []byte(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestSendAfterUndoWindow(t *testing.T) {
	e := newEnv(t, 0)
	e.start()
	id := e.submit(msg)
	if !e.hasEvent(Queued, id) {
		t.Error("no Queued event")
	}
	time.Sleep(30 * time.Millisecond)
	if n := len(e.srv.all()); n != 0 {
		t.Fatalf("sent %d messages inside the undo window", n)
	}
	e.eventually("sent", func() bool { return e.hasEvent(SentOK, id) })
	e.eventually("done", func() bool { return e.state(id) == Done })

	got := e.srv.all()
	if len(got) != 1 {
		t.Fatalf("server received %d messages", len(got))
	}
	r := got[0]
	if r.from != "me@example.org" || strings.Join(r.rcpts, ",") != "bob@example.com,liisa@example.com,secret@example.com" {
		t.Errorf("envelope: from %q, rcpts %v", r.from, r.rcpts)
	}
	if strings.Contains(r.data, "Bcc") || strings.Contains(r.data, "secret@") {
		t.Errorf("Bcc went over the wire:\n%s", r.data)
	}
	if !strings.Contains(r.data, "Message-Id: <") || !strings.Contains(r.data, "Date: ") || !strings.Contains(r.data, "Hei!\r\n") {
		t.Errorf("message not completed or not CRLF:\n%q", r.data)
	}
	if sent, _ := e.copier.counts(); sent != 1 || !strings.Contains(e.copier.sent[0], "Bcc: secret@example.com") {
		t.Errorf("Sent copy: %d copies, %q", sent, e.copier.sent)
	}
}

func TestUndoInTime(t *testing.T) {
	e := newEnv(t, 0)
	e.start()
	id := e.submit(msg)
	if err := e.ob.Undo(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if e.state(id) != Cancelled || !e.hasEvent(Undone, id) {
		t.Errorf("state %s", e.state(id))
	}
	if _, drafts := e.copier.counts(); drafts != 1 {
		t.Errorf("%d drafts saved, want 1", drafts)
	}
	time.Sleep(250 * time.Millisecond)
	if n := len(e.srv.all()); n != 0 {
		t.Errorf("an undone message was sent")
	}
}

func TestUndoTooLate(t *testing.T) {
	e := newEnv(t, 0)
	e.srv.block, e.srv.inData = make(chan struct{}), make(chan struct{})
	e.start()
	id := e.submit(msg)
	<-e.srv.inData // the server has the message, the reply is pending
	if err := e.ob.Undo(context.Background(), id); !errors.Is(err, ErrTooLate) {
		t.Errorf("Undo while sending: err = %v", err)
	}
	close(e.srv.block)
	e.eventually("done", func() bool { return e.state(id) == Done })
	if _, drafts := e.copier.counts(); drafts != 0 {
		t.Error("a sent message was also saved as a draft")
	}
}

func TestTemporaryFailureRetries(t *testing.T) {
	e := newEnv(t, 0)
	e.srv.rcptErr = &smtp.SMTPError{Code: 451, Message: "try later"}
	e.start()
	id := e.submit(msg)
	e.eventually("retrying", func() bool { return e.hasEvent(Retrying, id) })
	it, _ := e.ob.store.get(context.Background(), id)
	if it.State != Waiting || it.Attempts != 1 || !it.SendAfter.After(time.Now().Add(20*time.Second)) || !strings.Contains(it.Error, "451") {
		t.Errorf("after a 451: %+v", it)
	}
}

func TestPermanentFailureAndResend(t *testing.T) {
	e := newEnv(t, 0)
	e.srv.dataErr = &smtp.SMTPError{Code: 554, Message: "spam"}
	e.start()
	id := e.submit(msg)
	e.eventually("failed", func() bool { return e.hasEvent(FailedSend, id) })
	if e.state(id) != Failed {
		t.Fatalf("state %s", e.state(id))
	}
	e.srv.mu.Lock()
	e.srv.dataErr = nil
	e.srv.mu.Unlock()
	if err := e.ob.Resolve(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	e.eventually("resent", func() bool { return e.state(id) == Done })
}

func TestNoAnswerIsUncertainAndNeverResent(t *testing.T) {
	e := newEnv(t, 0)
	e.srv.hangUp = true
	e.start()
	id := e.submit(msg)
	e.eventually("uncertain", func() bool { return e.hasEvent(UncertainSend, id) })
	time.Sleep(200 * time.Millisecond)
	if n := len(e.srv.all()); n != 1 {
		t.Errorf("server got %d copies; an uncertain message must not be resent on its own", n)
	}
	if e.state(id) != Uncertain {
		t.Fatalf("state %s", e.state(id))
	}
	// The user checks and says it did go out: it is copied to Sent, not resent.
	if err := e.ob.Resolve(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	e.eventually("done", func() bool { return e.state(id) == Done })
	if sent, _ := e.copier.counts(); sent != 1 || len(e.srv.all()) != 1 {
		t.Errorf("after resolving: %d Sent copies, %d sends", sent, len(e.srv.all()))
	}
}

func TestCrashWhileSendingIsUncertain(t *testing.T) {
	e := newEnv(t, 0)
	id := e.submit(msg)
	if ok, err := e.ob.store.move(context.Background(), id, Sending, Waiting); !ok || err != nil {
		t.Fatal("couldn't fake a crash mid-send")
	}
	e.ob.Close()
	e.open() // restart
	e.start()
	e.eventually("uncertain after restart", func() bool { return e.hasEvent(UncertainSend, id) })
	if e.state(id) != Uncertain || len(e.srv.all()) != 0 {
		t.Errorf("state %s, %d sends", e.state(id), len(e.srv.all()))
	}
}

func TestSizeLimit(t *testing.T) {
	e := newEnv(t, 300)
	e.start()
	big := msg + strings.Repeat("x", 400) + "\n"
	id := e.submit(big)
	e.eventually("failed as too large", func() bool { return e.hasEvent(FailedSend, id) })
	// The limit is now known, so the next big message is refused at once.
	if _, err := e.ob.Submit(context.Background(), "home", []byte(big)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("second large message: err = %v", err)
	}
	if _, err := e.ob.Submit(context.Background(), "home", []byte(msg)); err != nil {
		t.Errorf("small message refused: %v", err)
	}
}

func TestCopyToSentIsRetried(t *testing.T) {
	e := newEnv(t, 0)
	e.copier.err = errors.New("IMAP is down")
	e.start()
	id := e.submit(msg)
	e.eventually("sent", func() bool { return e.hasEvent(SentOK, id) })
	e.eventually("copy failure recorded", func() bool {
		it, _ := e.ob.store.get(context.Background(), id)
		return it.State == Sent && strings.Contains(it.Error, "IMAP is down")
	})
	if n := len(e.srv.all()); n != 1 {
		t.Errorf("a failed copy caused %d sends", n)
	}
}

func TestPrepare(t *testing.T) {
	acct := account()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	p, err := prepare([]byte("To: bob@example.com\nSubject: Hi\n\nBody\n"), &acct, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(p.wire, []byte("From: \"Kim\" <me@example.org>")) && !bytes.Contains(p.wire, []byte("From: Kim <me@example.org>")) {
		t.Errorf("From not filled in:\n%s", p.wire)
	}
	if !strings.HasSuffix(p.messageID, "@example.org") || p.from != "me@example.org" || p.to != "bob@example.com" {
		t.Errorf("prepared = %+v", p)
	}

	p, _ = prepare([]byte("From: postmaster@example.org\nTo: bob@example.com\n\nx"), &acct, now)
	if p.from != "postmaster@example.org" {
		t.Errorf("alias as envelope sender: %q", p.from)
	}
	p, _ = prepare([]byte("From: someone@else.example\nTo: bob@example.com\n\nx"), &acct, now)
	if p.from != "me@example.org" {
		t.Errorf("foreign From must not be the envelope sender: %q", p.from)
	}
	if _, err := prepare([]byte("From: me@example.org\nSubject: nobody\n\nx"), &acct, now); err == nil {
		t.Error("a message without recipients was accepted")
	}
	p, _ = prepare([]byte("To: bob@example.com\nMessage-ID: <kept@x>\n\nx"), &acct, now)
	if p.messageID != "kept@x" {
		t.Errorf("existing Message-ID replaced: %q", p.messageID)
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	if backoff(20) > 64*time.Minute {
		t.Errorf("backoff(20) = %s", backoff(20))
	}
}

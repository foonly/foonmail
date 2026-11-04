package cache

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

type fixture struct {
	t       *testing.T
	db      *DB
	path    string
	account int64
	inbox   Folder
	archive Folder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "mail.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{t: t, db: db, path: path}
	f.account = get(db.EnsureAccount(ctx, "uplink"))
	f.inbox = get(db.EnsureFolder(ctx, f.account, "INBOX"))
	f.archive = get(db.EnsureFolder(ctx, f.account, "Archives.2026"))
	return f
}

// get returns v, or panics (failing the test) if err is set. Go only allows a
// multi-value call as the sole argument, so this can't take a *testing.T.
func get[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (f *fixture) count(table string) int {
	f.t.Helper()
	var n int
	if err := f.db.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func msg(uid uint32, msgID, subject, from string, flags ...string) NewMessage {
	return NewMessage{
		UID:   uid,
		Flags: flags,
		Size:  1000 + int64(len(subject)),
		Envelope: Envelope{
			MessageID: msgID,
			Subject:   subject,
			From:      Address{Name: from, Addr: strings.ToLower(strings.Fields(from)[0]) + "@example.com"},
			To:        []Address{{Name: "Niklas", Addr: "niklas@uplink.fi"}},
			Date:      time.Date(2026, 11, 1, 0, 0, int(uid), 0, time.UTC),
		},
	}
}

func TestOpenMigratesAndReopens(t *testing.T) {
	f := newFixture(t)
	var v int
	f.db.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != len(migrations) {
		t.Errorf("user_version = %d, want %d", v, len(migrations))
	}
	var mode string
	f.db.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %s, want wal", mode)
	}
	f.db.Close()

	db, err := Open(f.path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if id := get(db.EnsureAccount(ctx, "uplink")); id != f.account {
		t.Errorf("account id changed across reopen: %d != %d", id, f.account)
	}
}

func TestOpenReadOnly(t *testing.T) {
	f := newFixture(t)
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{msg(1, "a@x", "Hello", "Bob Example")}); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if id := get(ro.AccountID(ctx, "uplink")); id != f.account {
		t.Errorf("AccountID = %d", id)
	}
	if list := get(ro.List(ctx, f.inbox.ID, 10, 0)); len(list) != 1 || list[0].Subject != "Hello" {
		t.Errorf("read-only List = %+v", list)
	}
	// The daemon's writes show up for the reader.
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{msg(2, "b@x", "Later", "Bob Example")}); err != nil {
		t.Fatal(err)
	}
	if n := len(get(ro.UIDs(ctx, f.inbox.ID))); n != 2 {
		t.Errorf("reader sees %d messages after a write, want 2", n)
	}
	if _, err := ro.EnsureAccount(ctx, "other"); err == nil {
		t.Error("a read-only cache accepted a write")
	}
	if _, err := ro.AccountID(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown account: err = %v", err)
	}

	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "none.db")); !errors.Is(err, ErrNoCache) {
		t.Errorf("missing cache: err = %v", err)
	}
	f.db.db.Exec("PRAGMA user_version = 999")
	if _, err := OpenReadOnly(f.path); !errors.Is(err, ErrNoCache) {
		t.Errorf("other schema version: err = %v", err)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	f := newFixture(t)
	f.db.db.Exec("PRAGMA user_version = 999")
	f.db.Close()
	if _, err := Open(f.path); err == nil || !strings.Contains(err.Error(), "newer than this foonmail") {
		t.Errorf("err = %v", err)
	}
}

func TestAddListAndFlags(t *testing.T) {
	f := newFixture(t)
	err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{
		msg(10, "a@x", "Kahvitauko torstaina?", "Bob Example", `\Seen`),
		msg(11, "b@x", "Invoice 1234", "Liisa Mäkelä"),
		msg(12, "", "No message id", "Carol Smith", `\Flagged`, `\Seen`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if uids := get(f.db.UIDs(ctx, f.inbox.ID)); !slices.Equal(uids, []uint32{10, 11, 12}) {
		t.Errorf("UIDs = %v", uids)
	}
	if n := get(f.db.UnreadCount(ctx, f.inbox.ID)); n != 1 {
		t.Errorf("unread = %d, want 1", n)
	}
	list := get(f.db.List(ctx, f.inbox.ID, 10, 0))
	if len(list) != 3 || list[0].UID != 12 || list[2].UID != 10 {
		t.Fatalf("List order = %+v, want newest first", list)
	}
	if !slices.Equal(list[0].Flags, []string{`\Flagged`, `\Seen`}) || list[0].From.Name != "Carol Smith" {
		t.Errorf("summary = %+v", list[0])
	}

	// Adding a UID again updates its flags instead of duplicating it.
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{msg(11, "b@x", "Invoice 1234", "Liisa Mäkelä", `\Seen`)}); err != nil {
		t.Fatal(err)
	}
	if f.count("messages") != 3 || f.count("locations") != 3 {
		t.Errorf("re-adding a UID duplicated rows: %d messages, %d locations", f.count("messages"), f.count("locations"))
	}

	err = f.db.SetFlags(ctx, f.inbox.ID, []FlagUpdate{
		{UID: 10, Flags: nil, ModSeq: 7},
		{UID: 999, Flags: []string{`\Seen`}}, // unknown: ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	flags := get(f.db.Flags(ctx, f.inbox.ID))
	if len(flags[10]) != 0 || !slices.Equal(flags[11], []string{`\Seen`}) {
		t.Errorf("flags = %v", flags)
	}
	if _, ok := flags[999]; ok {
		t.Error("SetFlags created an unknown UID")
	}
}

func TestSameMessageInTwoFolders(t *testing.T) {
	f := newFixture(t)
	m := msg(10, "same@x", "Shared", "Bob Example")
	must1 := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must1(f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{m}))
	m.UID = 500
	must1(f.db.AddMessages(ctx, f.archive.ID, []NewMessage{m}))
	if f.count("messages") != 1 || f.count("locations") != 2 {
		t.Fatalf("%d messages, %d locations; want one message in two places", f.count("messages"), f.count("locations"))
	}

	// Same Message-ID but a different size is a different message.
	other := msg(11, "same@x", "Shared", "Bob Example")
	other.Size += 5
	must1(f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{other}))
	if f.count("messages") != 2 {
		t.Errorf("a different message with the same Message-ID was merged")
	}

	// Removing one location keeps the message; removing the last deletes it.
	must1(f.db.RemoveUIDs(ctx, f.inbox.ID, []uint32{10}))
	if f.count("messages") != 2 {
		t.Errorf("message deleted while still in Archive")
	}
	must1(f.db.RemoveUIDs(ctx, f.archive.ID, []uint32{500}))
	if f.count("messages") != 1 || f.count("message_text") != 1 {
		t.Errorf("orphan not cleaned up: %d messages, %d search rows", f.count("messages"), f.count("message_text"))
	}
}

func TestRemoveManyUIDs(t *testing.T) {
	f := newFixture(t)
	var msgs []NewMessage
	var uids []uint32
	for i := uint32(1); i <= 1200; i++ {
		msgs = append(msgs, msg(i, "", "Bulk", "Bob Example"))
		if i%2 == 0 {
			uids = append(uids, i)
		}
	}
	if err := f.db.AddMessages(ctx, f.inbox.ID, msgs); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RemoveUIDs(ctx, f.inbox.ID, uids); err != nil {
		t.Fatal(err)
	}
	if n := len(get(f.db.UIDs(ctx, f.inbox.ID))); n != 600 {
		t.Errorf("%d UIDs left, want 600", n)
	}
	if f.count("messages") != 600 {
		t.Errorf("%d messages left, want 600", f.count("messages"))
	}
}

func TestResetFolderOnUIDValidityChange(t *testing.T) {
	f := newFixture(t)
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{msg(1, "a@x", "A", "Bob Example"), msg(2, "b@x", "B", "Bob Example")}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SaveSyncState(ctx, f.inbox.ID, 100, 55, 3); err != nil {
		t.Fatal(err)
	}
	got := get(f.db.EnsureFolder(ctx, f.account, "INBOX"))
	if got.UIDValidity != 100 || got.HighestModSeq != 55 || got.UIDNext != 3 || got.SyncedAt.IsZero() {
		t.Errorf("sync state = %+v", got)
	}

	if err := f.db.ResetFolder(ctx, f.inbox.ID, 200); err != nil {
		t.Fatal(err)
	}
	got = get(f.db.EnsureFolder(ctx, f.account, "INBOX"))
	if got.UIDValidity != 200 || got.HighestModSeq != 0 || got.UIDNext != 0 {
		t.Errorf("after reset = %+v", got)
	}
	if f.count("locations") != 0 || f.count("messages") != 0 {
		t.Errorf("reset left %d locations, %d messages", f.count("locations"), f.count("messages"))
	}
}

func TestFolderByIDAndLocation(t *testing.T) {
	f := newFixture(t)
	got := get(f.db.FolderByID(ctx, f.archive.ID))
	if got.Name != "Archives.2026" || got.AccountID != f.account {
		t.Errorf("FolderByID = %+v", got)
	}
	if _, err := f.db.FolderByID(ctx, 9999); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown folder: err = %v", err)
	}
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{msg(7, "a@x", "A", "Bob Example")}); err != nil {
		t.Fatal(err)
	}
	id := get(f.db.Location(ctx, f.inbox.ID, 7))
	if h := get(f.db.Header(ctx, id)); h.Subject != "A" {
		t.Errorf("Location points at %+v", h)
	}
	if _, err := f.db.Location(ctx, f.inbox.ID, 8); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown UID: err = %v", err)
	}
}

func TestEntriesAndCounts(t *testing.T) {
	f := newFixture(t)
	reply := msg(2, "reply@x", "Re: A", "Bob Example")
	reply.Envelope.InReplyTo = "a@x"
	reply.Envelope.References = []string{"root@x", "a@x"}
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{msg(1, "a@x", "A", "Bob Example", `\Seen`), reply}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AddMessages(ctx, f.archive.ID, []NewMessage{msg(9, "old@x", "Old", "Bob Example", `\Seen`)}); err != nil {
		t.Fatal(err)
	}
	es := get(f.db.Entries(ctx, f.inbox.ID))
	if len(es) != 2 || es[0].UID != 2 || es[0].InReplyTo != "a@x" || len(es[0].References) != 2 || es[1].MessageID != "a@x" {
		t.Errorf("Entries = %+v", es)
	}
	counts := get(f.db.FolderCounts(ctx, f.account))
	if counts[f.inbox.ID] != (Counts{Total: 2, Unread: 1}) || counts[f.archive.ID] != (Counts{Total: 1, Unread: 0}) {
		t.Errorf("FolderCounts = %+v", counts)
	}
}

func TestDeleteFolder(t *testing.T) {
	f := newFixture(t)
	shared := msg(1, "shared@x", "In both", "Bob Example")
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{shared}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AddMessages(ctx, f.archive.ID, []NewMessage{shared, msg(2, "only@x", "Only here", "Bob Example")}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.DeleteFolder(ctx, f.archive.ID); err != nil {
		t.Fatal(err)
	}
	folders := get(f.db.Folders(ctx, f.account))
	if len(folders) != 1 || folders[0].Name != "INBOX" {
		t.Errorf("folders = %+v", folders)
	}
	if f.count("messages") != 1 || f.count("locations") != 1 {
		t.Errorf("%d messages, %d locations; want only the INBOX copy left", f.count("messages"), f.count("locations"))
	}
}

func TestSearch(t *testing.T) {
	f := newFixture(t)
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{
		msg(1, "1@x", "Kahvitauko torstaina?", "Bob Example"),
		msg(2, "2@x", "Käsi kipeä", "Liisa Mäkelä"),
		msg(3, "3@x", "Kasi on numero", "Pekka Virtanen"),
		msg(4, "4@x", `Quotes "and" stars* OR NEAR(x)`, "Carol Smith"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.AddMessages(ctx, f.archive.ID, []NewMessage{msg(9, "9@x", "Kahvi arkistossa", "Bob Example")}); err != nil {
		t.Fatal(err)
	}

	uids := func(folderID int64, words ...string) []uint32 {
		t.Helper()
		res, err := f.db.Search(ctx, f.account, folderID, words, 50)
		if err != nil {
			t.Fatalf("Search(%q): %v", words, err)
		}
		var out []uint32
		for _, s := range res {
			out = append(out, s.UID)
		}
		slices.Sort(out)
		return out
	}
	cases := []struct {
		folder int64
		words  []string
		want   []uint32
	}{
		{f.inbox.ID, []string{"kahv"}, []uint32{1}},             // prefix, current folder only
		{0, []string{"kahv"}, []uint32{1, 9}},                   // all folders
		{0, []string{"kahv", "bob"}, []uint32{1, 9}},            // subject and sender
		{0, []string{"kahv", "arkisto"}, []uint32{9}},           // AND
		{0, []string{"käsi"}, []uint32{2}},                      // ä kept distinct from a
		{0, []string{"kasi"}, []uint32{3}},                      //
		{0, []string{"mäkelä"}, []uint32{2}},                    // sender name
		{0, []string{"niklas@uplink"}, []uint32{1, 2, 3, 4, 9}}, // recipients
		{0, []string{`"and"`}, []uint32{4}},                     // quotes are searched literally
		{0, []string{"OR"}, []uint32{4}},                        // FTS keywords are plain words
		{0, []string{"NEAR(x)"}, []uint32{4}},                   //
		{0, []string{"  "}, nil},                                // nothing to search
	}
	for _, c := range cases {
		if got := uids(c.folder, c.words...); !slices.Equal(got, c.want) {
			t.Errorf("Search(folder %d, %q) = %v, want %v", c.folder, c.words, got, c.want)
		}
	}
}

func TestRawAndBodyText(t *testing.T) {
	f := newFixture(t)
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{msg(1, "1@x", "Hello", "Bob Example")}); err != nil {
		t.Fatal(err)
	}
	s := get(f.db.List(ctx, f.inbox.ID, 1, 0))[0]
	if s.Fetched {
		t.Error("Fetched before any raw was stored")
	}
	if raw := get(f.db.Raw(ctx, s.ID)); raw != nil {
		t.Errorf("Raw before fetch = %q", raw)
	}
	raw := []byte("Subject: Hello\r\n\r\nPöytävaraus on klo 14.\r\n")
	if err := f.db.SetRaw(ctx, s.ID, raw, "Pöytävaraus on klo 14."); err != nil {
		t.Fatal(err)
	}
	if got := get(f.db.Raw(ctx, s.ID)); string(got) != string(raw) {
		t.Errorf("Raw = %q", got)
	}
	if !get(f.db.List(ctx, f.inbox.ID, 1, 0))[0].Fetched {
		t.Error("Fetched not set after SetRaw")
	}
	// Header search doesn't look at the body.
	if res := get(f.db.Search(ctx, f.account, 0, []string{"pöytävaraus"}, 10)); len(res) != 0 {
		t.Errorf("header search matched body text: %+v", res)
	}
	var n int
	f.db.db.QueryRow(`SELECT count(*) FROM message_text WHERE message_text MATCH 'body : pöytävaraus'`).Scan(&n)
	if n != 1 {
		t.Errorf("body text not indexed")
	}
	if err := f.db.SetRaw(ctx, 9999, raw, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SetRaw on unknown message: err = %v", err)
	}
}

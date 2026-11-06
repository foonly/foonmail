package cache

import "testing"

func TestEncodedHeadersAreDecoded(t *testing.T) {
	f := newFixture(t)
	m := msg(1, "a@x", "=?windows-1252?Q?F=F6delsedagsm=E5nad_=96_h=E4r?=", "Club One")
	m.Envelope.From.Name = "=?ISO-8859-15?Q?Sch=F6nberg_Niklas?="
	m.Envelope.To = []Address{{Name: "=?windows-1252?Q?Viktor_B=E4ck?=", Addr: "viktor@example.com"}}
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{m}); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		var subject, from, to string
		f.db.db.QueryRow(`SELECT subject, from_name, to_list FROM messages`).Scan(&subject, &from, &to)
		if subject != "Födelsedagsmånad – här" || from != "Schönberg Niklas" || to != "Viktor Bäck <viktor@example.com>" {
			t.Errorf("%s: stored %q, %q, %q", when, subject, from, to)
		}
		res, err := f.db.Search(ctx, f.account, 0, []string{"födelse", "bäck"}, 10)
		if err != nil || len(res) != 1 {
			t.Errorf("%s: search found %v, %v", when, res, err)
		}
	}
	check("added")

	// A cache written before the decoder knew windows-1252 holds them
	// encoded; opening it for writing decodes them.
	if _, err := f.db.db.Exec(`UPDATE messages SET subject = ?, from_name = ?, to_list = ?`,
		m.Envelope.Subject, m.Envelope.From.Name, joinAddrs(m.Envelope.To)); err != nil {
		t.Fatal(err)
	}
	f.db.db.Exec(`UPDATE message_text SET subject = ?, people = ''`, m.Envelope.Subject)
	f.db.Close()
	db, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	t.Cleanup(func() { db.Close() })
	check("reopened")
}

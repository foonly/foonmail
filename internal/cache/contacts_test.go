package cache

import (
	"testing"
	"time"
)

func TestCorrespondents(t *testing.T) {
	f := newFixture(t)
	day := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	add := func(uid uint32, from Address, to []Address, cc []Address, d int) NewMessage {
		return NewMessage{UID: uid, Envelope: Envelope{MessageID: string(rune('a'+uid)) + "@x", From: from, To: to, Cc: cc,
			Date: day.AddDate(0, 0, d)}}
	}
	me := Address{Name: "Kim", Addr: "Me@example.org"}
	bob := Address{Name: "Bob", Addr: "bob@example.com"}
	if err := f.db.AddMessages(ctx, f.inbox.ID, []NewMessage{
		add(1, bob, []Address{me}, nil, 0),
		add(2, Address{Name: "Bob Example", Addr: "BOB@example.com"}, []Address{me}, []Address{{Addr: "liisa@example.fi"}}, 2),
		add(3, me, []Address{{Name: "Ström, Kim", Addr: "kim.strom@example.net"}, bob}, []Address{{Name: "Liisa M", Addr: "liisa@example.fi"}}, 1),
		add(4, Address{Addr: "no-reply@shop.example"}, []Address{me}, nil, 3),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := f.db.Correspondents(ctx, f.account, []string{"me@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	byAddr := map[string]Correspondent{}
	for _, c := range got {
		byAddr[c.Addr] = c
	}
	if len(got) != 4 {
		t.Fatalf("%d correspondents: %+v", len(got), got)
	}
	if b := byAddr["bob@example.com"]; b.Sent != 1 || b.Received != 2 || b.Name != "Bob Example" || !b.Last.Equal(day.AddDate(0, 0, 2)) {
		t.Errorf("bob: %+v", b)
	}
	if l := byAddr["liisa@example.fi"]; l.Sent != 1 || l.Received != 0 || l.Name != "Liisa M" {
		t.Errorf("liisa (only cc'd by others counts for nothing): %+v", l)
	}
	if k := byAddr["kim.strom@example.net"]; k.Sent != 1 || k.Name == "" {
		t.Errorf("a name with a comma: %+v", k)
	}
}

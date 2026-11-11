package render

import (
	"strings"
	"testing"
	"time"
)

// gmailChain is shaped like a reply from Gmail on a phone that quotes the
// three messages before it, each "On …, time Name, <addr> wrote:".
const gmailChain = "Let's bring Liisa into this.\n\nKim Example\n\n" +
	"On Wed, 11 Nov 2026, 16.32 Bob Example, <bob@example.com> wrote:\n\n" +
	"> This is the third message\n>\n> Happy 😁\n>\n" +
	"> On Wed, 11 Nov 2026, 16.32 Kim Example, <kim@example.org> wrote:\n>\n" +
	">> This is the second message\n>>\n>> Kim Example\n>>\n" +
	">> On Wed, 11 Nov 2026, 16.31 Bob Example, <bob@example.com> wrote:\n>>\n" +
	">>> This is the first message.\n>>>\n>>\n"

func plainMessage(t *testing.T, text string) *Message {
	t.Helper()
	return mustRender(t, raw([]string{"Content-Type: text/plain; charset=utf-8"}, text), Options{})
}

func TestRebuildGmailChain(t *testing.T) {
	m := plainMessage(t, gmailChain)
	if q := m.Quotes[0]; q.Who != "Bob Example" {
		t.Errorf("quote's Who = %q", q.Who)
	}
	got := Rebuild(m)
	want := []Rebuilt{
		{Who: "Bob Example", Addr: "bob@example.com", When: "Wed, 11 Nov 2026, 16.31", Text: "This is the first message."},
		{Who: "Kim Example", Addr: "kim@example.org", When: "Wed, 11 Nov 2026, 16.32", Text: "This is the second message\n\nKim Example"},
		{Who: "Bob Example", Addr: "bob@example.com", When: "Wed, 11 Nov 2026, 16.32", Text: "This is the third message\n\nHappy 😁"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d messages: %+v", len(got), got)
	}
	for i := range want {
		g := got[i]
		g.Date = time.Time{}
		if g != want[i] {
			t.Errorf("message %d:\n got %+v\nwant %+v", i, g, want[i])
		}
	}
	if d := got[0].Date; d.IsZero() || d.Hour() != 16 || d.Minute() != 31 || d.Day() != 11 {
		t.Errorf("first date = %v", d)
	}
}

func TestRebuildFromHTML(t *testing.T) {
	html := `<div>Count me in.</div><div class="gmail_quote"><div class="gmail_attr">On Tue, 3 Nov 2026 at 14:02, Bob Example &lt;<a href="mailto:bob@example.com">bob@example.com</a>&gt; wrote:<br></div>` +
		`<blockquote class="gmail_quote"><div>Coffee on Thursday?</div></blockquote></div>`
	m := mustRender(t, raw([]string{"Content-Type: text/html; charset=utf-8"}, html), Options{})
	got := Rebuild(m)
	if len(got) != 1 || got[0].Who != "Bob Example" || got[0].Addr != "bob@example.com" || got[0].Text != "Coffee on Thursday?" {
		t.Fatalf("got %+v\nfrom:\n%s", got, m.Text)
	}
	if got[0].When != "Tue, 3 Nov 2026 at 14:02" || got[0].Date.IsZero() {
		t.Errorf("when %q, date %v", got[0].When, got[0].Date)
	}
}

func TestRebuildOutlook(t *testing.T) {
	text := "Thanks!\n\n________________________________\nFrom: Bob Example <bob@example.com>\nSent: Tuesday, November 3, 2026 2:02 PM\n" +
		"To: Kim\nSubject: Report\n\nPlease send it.\n\n" +
		"________________________________\nFrom: Liisa <liisa@example.com>\nSent: Monday, November 2, 2026 9:15 AM\n" +
		"To: Bob Example\nSubject: Report\n\nIs the report done?"
	got := Rebuild(plainMessage(t, text))
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if g := got[0]; g.Who != "Liisa" || g.Addr != "liisa@example.com" || g.Text != "Is the report done?" || g.Date.Day() != 2 {
		t.Errorf("oldest = %+v", g)
	}
	if g := got[1]; g.Who != "Bob Example" || g.Text != "Please send it." || g.When != "Tuesday, November 3, 2026 2:02 PM" || g.Date.Hour() != 14 {
		t.Errorf("newer = %+v", g)
	}
}

func TestRebuildOther(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string // Who: Text, oldest first
	}{
		{"inline answers", "> First question?\nAnswer one.\n\n> Second question?\nAnswer two.\n", nil},
		{"no attribution", "Yes.\n\n> Question\n", nil},
		{"address only", "Sure.\n\nOn Tue, 3 Nov 2026 at 14:02, <bob@example.com> wrote:\n> Coffee?\n",
			[]string{"bob@example.com: Coffee?"}},
		{"swedish", "Ja.\n\nDen tis 3 nov. 2026 kl 14:02 skrev Anna Söderström <anna@example.se>:\n> Fika?\n",
			[]string{"Anna Söderström: Fika?"}},
		{"finnish", "Käy.\n\nti 3.11.2026 klo 14.02 Liisa Mäkelä (liisa@example.com) kirjoitti:\n> Kahville?\n",
			[]string{"Liisa Mäkelä: Kahville?"}},
		{"inline answers inside the quote stop the chain",
			"OK.\n\nOn Tue, 3 Nov 2026 at 14:02, Bob <bob@example.com> wrote:\n> > Coffee?\n> Yes, at ten.\n>\n> > Where?\n> Downstairs.\n",
			[]string{"Bob: > Coffee?\nYes, at ten.\n\n> Where?\nDownstairs."}},
		{"hidden quote",
			"OK.\n\nOn Tue, 3 Nov 2026 at 14:02, Bob <bob@example.com> wrote:\n> Fine by me.\n> [Quoted text hidden]\n",
			[]string{"Bob: Fine by me."}},
	}
	for _, c := range cases {
		var got []string
		for _, r := range Rebuild(plainMessage(t, c.text)) {
			got = append(got, r.Who+": "+r.Text)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	if d := parseWhen("ti 3.11.2026 klo 14.02"); d.IsZero() || d.Month() != time.November {
		t.Errorf("finnish date = %v", d)
	}
}

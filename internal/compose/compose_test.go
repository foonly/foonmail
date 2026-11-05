package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"foonmail/internal/config"
	"foonmail/internal/render"
)

func acct() *config.Account {
	return &config.Account{Name: "home", Email: "me@example.org", DisplayName: "Kim Ström",
		Aliases: []string{"postmaster@example.org"}}
}

var (
	bob   = render.Address{Name: "Bob", Addr: "bob@example.com"}
	liisa = render.Address{Name: "Mäkelä, Liisa", Addr: "liisa@example.com"}
	me    = render.Address{Name: "Kim", Addr: "me@example.org"}
	alias = render.Address{Addr: "postmaster@example.org"}
)

func original() *render.Message {
	return &render.Message{
		Subject: "Kahvi?", From: []render.Address{bob}, To: []render.Address{me, liisa},
		Cc:        []render.Address{{Addr: "BOB@example.com"}, {Addr: "pekka@example.com"}},
		Date:      time.Date(2026, 11, 4, 14, 2, 0, 0, time.Local),
		MessageID: "c@x", References: []string{"a@x", "b@x"},
		Text: "Torstaina?\n\n> earlier\nBob",
	}
}

func TestReplySender(t *testing.T) {
	d := Reply(acct(), original(), ReplySender)
	if d.To != "Bob <bob@example.com>" || d.Cc != "" {
		t.Errorf("To %q, Cc %q", d.To, d.Cc)
	}
	if d.From != "Kim Ström <me@example.org>" {
		t.Errorf("From = %q", d.From)
	}
	if d.Subject != "Re: Kahvi?" || d.InReplyTo != "c@x" || !slices.Equal(d.References, []string{"a@x", "b@x", "c@x"}) {
		t.Errorf("subject %q, in-reply-to %q, refs %v", d.Subject, d.InReplyTo, d.References)
	}
	want := "\n\nOn Wed, 4 Nov 2026 at 14:02, Bob <bob@example.com> wrote:\n> Torstaina?\n>\n>> earlier\n> Bob\n"
	if d.Body != want {
		t.Errorf("body:\n%q\nwant\n%q", d.Body, want)
	}
}

func TestReplyAll(t *testing.T) {
	d := Reply(acct(), original(), ReplyAll)
	if d.To != "Bob <bob@example.com>" {
		t.Errorf("To = %q", d.To)
	}
	// Not me, not Bob again (case-insensitive), the comma in Liisa's name quoted.
	if d.Cc != `"Mäkelä, Liisa" <liisa@example.com>, pekka@example.com` {
		t.Errorf("Cc = %q", d.Cc)
	}
}

func TestReplyToHeaderAndAlias(t *testing.T) {
	m := original()
	m.ReplyTo = []render.Address{{Addr: "list@example.com"}}
	m.To = []render.Address{alias}
	d := Reply(acct(), m, ReplySender)
	if d.To != "list@example.com" {
		t.Errorf("Reply-To not used: %q", d.To)
	}
	if d.From != "Kim Ström <postmaster@example.org>" {
		t.Errorf("mail to an alias should be answered from it: %q", d.From)
	}
}

func TestReplyToOwnMessage(t *testing.T) {
	m := original()
	m.From = []render.Address{me}
	m.To = []render.Address{bob}
	m.Cc = []render.Address{liisa}
	if d := Reply(acct(), m, ReplySender); d.To != "Bob <bob@example.com>" || d.Cc != "" {
		t.Errorf("reply to own message: To %q, Cc %q", d.To, d.Cc)
	}
	if d := Reply(acct(), m, ReplyAll); d.Cc != `"Mäkelä, Liisa" <liisa@example.com>` {
		t.Errorf("reply-all to own message: Cc %q", d.Cc)
	}
}

func TestSubjectPrefixes(t *testing.T) {
	cases := []struct{ prefix, in, want string }{
		{"Re: ", "Kahvi?", "Re: Kahvi?"},
		{"Re: ", "Re: Kahvi?", "Re: Kahvi?"},
		{"Re: ", "VS: Kokous", "VS: Kokous"},
		{"Re: ", "Fwd: Link", "Re: Fwd: Link"},
		{"Fwd: ", "Re: Plan", "Fwd: Re: Plan"},
		{"Fwd: ", "Fw: Plan", "Fw: Plan"},
		{"Re: ", "Reunion", "Re: Reunion"},
	}
	for _, c := range cases {
		if got := prefixed(c.prefix, c.in); got != c.want {
			t.Errorf("prefixed(%q, %q) = %q, want %q", c.prefix, c.in, got, c.want)
		}
	}
}

func TestBuildRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "lasku ä.pdf")
	os.WriteFile(file, []byte("%PDF fake"), 0o600)
	d := Reply(acct(), original(), ReplyAll)
	d.Bcc = "secret@example.com"
	d.Body = "Käy!\n" + d.Body
	d.Attachments = []Attachment{{Name: "lasku ä.pdf", Path: file}, {Name: "notes.txt", Data: []byte("hi")}}
	raw, err := Build(d)
	if err != nil {
		t.Fatal(err)
	}
	m, err := render.Render(raw, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m.From[0] != (render.Address{Name: "Kim Ström", Addr: "me@example.org"}) {
		t.Errorf("From = %+v", m.From)
	}
	if len(m.Cc) != 2 || m.Cc[0].Name != "Mäkelä, Liisa" || m.Subject != "Re: Kahvi?" {
		t.Errorf("Cc %+v, subject %q", m.Cc, m.Subject)
	}
	if m.MessageID != "" || !slices.Equal(m.References, []string{"a@x", "b@x", "c@x"}) {
		t.Errorf("Message-ID %q (the daemon sets it), References %v", m.MessageID, m.References)
	}
	if !strings.HasPrefix(m.Text, "Käy!") || len(m.Attachments) != 2 || m.Attachments[0].Filename != "lasku ä.pdf" {
		t.Errorf("text %q, attachments %+v", m.Text, m.Attachments)
	}
	if !strings.Contains(string(raw), "In-Reply-To: <c@x>") || !strings.Contains(string(raw), "Bcc: <secret@example.com>") {
		t.Errorf("headers missing:\n%s", raw[:400])
	}
}

func TestBuildErrors(t *testing.T) {
	d := New(acct())
	if _, err := Build(d); err == nil || !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("no recipients: err = %v", err)
	}
	d.To = "not an address"
	if _, err := Build(d); err == nil || !strings.Contains(err.Error(), "To:") {
		t.Errorf("bad To: err = %v", err)
	}
	d.To = "bob@example.com"
	d.Attachments = []Attachment{{Name: "gone", Path: "/definitely/missing"}}
	if _, err := Build(d); err == nil {
		t.Error("a missing attachment file was accepted")
	}
}

func TestForward(t *testing.T) {
	pdf := "%PDF-1.4 fake"
	raw := "From: Bob <bob@example.com>\r\nTo: me@example.org\r\nSubject: Re: Report\r\nMessage-ID: <r@x>\r\n" +
		"Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nSee attached.\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=report.pdf\r\n\r\n" + pdf + "\r\n--b--\r\n"
	m, err := render.Render([]byte(raw), render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Forward(acct(), m, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != "Fwd: Re: Report" || d.To != "" || d.InReplyTo != "" {
		t.Errorf("subject %q, to %q, in-reply-to %q", d.Subject, d.To, d.InReplyTo)
	}
	if !strings.Contains(d.Body, "---------- Forwarded message ----------\nFrom: Bob <bob@example.com>") ||
		!strings.Contains(d.Body, "See attached.") {
		t.Errorf("body:\n%s", d.Body)
	}
	if len(d.Attachments) != 1 || d.Attachments[0].Name != "report.pdf" || string(d.Attachments[0].Data) != pdf {
		t.Errorf("attachments = %+v", d.Attachments)
	}
}

func TestEditorFileRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(file, []byte("x"), 0o600)
	d := Reply(acct(), original(), ReplyAll)
	d.Attachments = []Attachment{{Name: "a.txt", Path: file}, {Name: "fwd.pdf", Data: []byte("pdf")}}
	got, problems := ParseEditorFile(EditorFile(d), d)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if got.To != d.To || got.Cc != d.Cc || got.From != d.From || got.Subject != d.Subject ||
		got.Body != strings.TrimSuffix(d.Body, "\n") || got.InReplyTo != "c@x" {
		t.Errorf("round trip changed the draft:\n%+v\n%+v", got, d)
	}
	if len(got.Attachments) != 2 || string(got.Attachments[1].Data) != "pdf" {
		t.Errorf("attachments = %+v", got.Attachments)
	}

	edited := strings.Replace(string(EditorFile(d)), "Attach: forwarded: fwd.pdf\n",
		"Attach: forwarded: nope.pdf\nAttach: /missing/file\nX-Bad: 1\n", 1)
	_, problems = ParseEditorFile([]byte(edited), d)
	if len(problems) != 3 {
		t.Errorf("problems = %q", problems)
	}
	// Quoted Attach: lines in the body are text, not headers.
	if got, problems := ParseEditorFile([]byte("To: bob@example.com\n\n> Attach: /etc/passwd\n"), New(acct())); len(problems) > 0 || len(got.Attachments) != 0 {
		t.Errorf("body Attach: line treated as a header: %v %+v", problems, got.Attachments)
	}
}

func TestDraftRoundTrip(t *testing.T) {
	d := Reply(acct(), original(), ReplyAll)
	d.Bcc = "secret@example.com"
	d.Body = "Half written\n\n" + d.Body
	d.Attachments = []Attachment{{Name: "plan.txt", Data: []byte("step 1")}}
	raw, err := BuildDraft(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.From != d.From || got.To != d.To || got.Cc != d.Cc || got.Bcc != "secret@example.com" || got.Subject != d.Subject {
		t.Errorf("addresses or subject changed:\n got %+v\nwant %+v", got, d)
	}
	if got.InReplyTo != "c@x" || !slices.Equal(got.References, d.References) {
		t.Errorf("threading lost: %q %v", got.InReplyTo, got.References)
	}
	if !strings.HasPrefix(got.Body, "Half written") || !strings.Contains(got.Body, "> Torstaina?") {
		t.Errorf("body = %q", got.Body)
	}
	if len(got.Attachments) != 1 || string(got.Attachments[0].Data) != "step 1" {
		t.Errorf("attachments = %+v", got.Attachments)
	}
}

func TestDraftWithoutRecipientsOrWithTypos(t *testing.T) {
	d := New(acct())
	d.Subject = "Ideas"
	d.Body = "Nothing yet"
	if _, err := Build(d); err == nil {
		t.Fatal("Build should still insist on recipients")
	}
	raw, err := BuildDraft(d)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := FromMessage(raw)
	if got.To != "" || got.Subject != "Ideas" {
		t.Errorf("empty draft came back as To %q, Subject %q", got.To, got.Subject)
	}

	d.To = "bob@example.com, liisa@exam" // still being typed
	d.Cc = "Pekka <pekka@"
	raw, err = BuildDraft(d)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = FromMessage(raw)
	if got.To != "bob@example.com, liisa@exam" || got.Cc != "Pekka <pekka@" {
		t.Errorf("half-typed fields: To %q, Cc %q", got.To, got.Cc)
	}
}

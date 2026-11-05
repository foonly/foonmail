package render

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// raw builds a message from header lines and a body, with CRLF line ends.
func raw(headers []string, body string) []byte {
	h := strings.Join(headers, "\r\n")
	return []byte(h + "\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n"))
}

func mustRender(t *testing.T, msg []byte, opts Options) *Message {
	t.Helper()
	m, err := Render(msg, opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPlainMessageAndHeaders(t *testing.T) {
	m := mustRender(t, raw([]string{
		"From: =?utf-8?q?Liisa_M=C3=A4kel=C3=A4?= <liisa@example.com>",
		"To: Niklas <niklas@uplink.fi>, bob@example.com",
		"Subject: =?utf-8?b?S2FodmkgdG9yc3RhaW5hPw==?=",
		"Date: Wed, 04 Nov 2026 12:00:00 +0200",
		"Content-Type: text/plain; charset=utf-8",
	}, "Hei!\n\nPöytävaraus on klo 14.   \n\n\n\nTerveisin,\nLiisa\n"), Options{})

	if m.Subject != "Kahvi torstaina?" || m.From[0] != (Address{"Liisa Mäkelä", "liisa@example.com"}) {
		t.Errorf("headers: %q %+v", m.Subject, m.From)
	}
	if len(m.To) != 2 || m.To[1].Addr != "bob@example.com" || m.Date.Day() != 4 {
		t.Errorf("To %+v, Date %v", m.To, m.Date)
	}
	want := "Hei!\n\nPöytävaraus on klo 14.\n\nTerveisin,\nLiisa"
	if m.Text != want || m.Source != SourcePlain {
		t.Errorf("text = %q (%s), want %q", m.Text, m.Source, want)
	}
}

func TestLegacyCharset(t *testing.T) {
	body := []byte("P\xf6yt\xe4varaus \xa4 5") // ISO-8859-15: ö, ä, €
	msg := append([]byte("Subject: =?iso-8859-1?q?P=E4iv=E4?=\r\nContent-Type: text/plain; charset=iso-8859-15\r\n\r\n"), body...)
	m := mustRender(t, msg, Options{})
	if m.Subject != "Päivä" || m.Text != "Pöytävaraus € 5" {
		t.Errorf("subject %q, text %q", m.Subject, m.Text)
	}
}

func TestUnknownCharsetStillRenders(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/plain; charset=x-made-up"}, "hello"), Options{})
	if m.Text != "hello" || len(m.Warnings) == 0 {
		t.Errorf("text %q, warnings %v", m.Text, m.Warnings)
	}
}

func TestFormatFlowed(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/plain; charset=utf-8; format=flowed; delsp=no"},
		"This paragraph was sent from a phone and \nsoft-wrapped at seventy-two \ncolumns.\n\n"+
			"> A quoted line that is also \n> flowed.\n>> Deeper.\n \n-- \nSig"),
		Options{})
	want := "This paragraph was sent from a phone and soft-wrapped at seventy-two columns.\n\n" +
		"> A quoted line that is also flowed.\n>> Deeper.\n\n-- \nSig"
	if m.Text != strings.Replace(want, "-- \n", "--\n", 1) {
		t.Errorf("flowed:\n%q\nwant\n%q", m.Text, want)
	}
}

func TestFlowedDelSp(t *testing.T) {
	got := decodeFlowed("Lang \nwort", true)
	if got != "Langwort" {
		t.Errorf("delsp: %q", got)
	}
}

func multipartAlt(plain, html string) []byte {
	return raw([]string{
		"From: News <news@example.com>",
		"Subject: Newsletter",
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="b1"`,
	}, "--b1\nContent-Type: text/plain; charset=utf-8\n\n"+plain+
		"\n--b1\nContent-Type: text/html; charset=utf-8\n\n"+html+"\n--b1--\n")
}

const newsletterHTML = `<html><head><title>T</title><style>p{color:red}</style></head><body>
<div style="display:none;max-height:0;overflow:hidden">Preview text that must not show` + "\u034f\u200c" + `</div>
<span style="font-size:0px">tiny hidden</span>
<h1>Big   news</h1>
<p>Read our <a href="https://example.com/post">latest post</a> and
<a href="https://example.com/post">again</a>. Also <a href="mailto:hi@example.com">mail us</a>,
<a href="#top">top</a>, <a href="javascript:alert(1)">click</a>.</p>
<script>alert("x")</script>
<table><tr><td>Cell A</td><td>Cell B</td></tr><tr><td><img src="logo.png" alt="Logo"><img src="pixel.gif" width="1"></td></tr></table>
<ul><li>One</li><li>Two<ol><li>Nested</li></ol></li></ul>
<p>Line one<br>line two<br><br>after blank</p>
<pre>  code
    indented</pre>
<p>Unsubscribe <a href="https://example.com/u"><img src="b.png" alt="here"></a></p>
</body></html>`

func TestHTMLConversion(t *testing.T) {
	m := mustRender(t, multipartAlt("", newsletterHTML), Options{})
	if m.Source != SourceHTML || !m.HasPlain || !m.HasHTML {
		t.Fatalf("source %s plain %v html %v", m.Source, m.HasPlain, m.HasHTML)
	}
	want := strings.Join([]string{
		"Big news",
		"",
		"Read our latest post [1] and again [1]. Also mail us [2], top, click.",
		"",
		"Cell A Cell B",
		"[Logo]",
		"",
		"• One",
		"• Two",
		"  1. Nested",
		"",
		"Line one",
		"line two",
		"",
		"after blank",
		"",
		"  code",
		"    indented",
		"",
		"Unsubscribe [here] [3]",
	}, "\n")
	if m.Text != want {
		t.Errorf("converted:\n%s\n---- want ----\n%s", m.Text, want)
	}
	if len(m.Links) != 3 || m.Links[0].URL != "https://example.com/post" || m.Links[1].URL != "mailto:hi@example.com" {
		t.Errorf("links = %+v", m.Links)
	}
	for _, bad := range []string{"Preview", "tiny hidden", "alert", "color:red", "\u034f"} {
		if strings.Contains(m.Text, bad) {
			t.Errorf("text contains %q", bad)
		}
	}
	if !strings.Contains(m.HTML, "<h1>") {
		t.Error("raw HTML not kept for the browser view")
	}
}

func TestPlainPreferredUnlessStub(t *testing.T) {
	good := "Hi Niklas,\n\nThe meeting moved to Thursday. Let me know if that doesn't work for you.\n\nBob"
	m := mustRender(t, multipartAlt(good, "<p>Hi Niklas, the meeting moved.</p>"), Options{})
	if m.Source != SourcePlain || !strings.HasPrefix(m.Text, "Hi Niklas,") {
		t.Errorf("a real text part should win: %s %q", m.Source, m.Text)
	}
	if m := mustRender(t, multipartAlt(good, "<p>Hi</p>"), Options{PreferHTML: true}); m.Source != SourceHTML {
		t.Error("PreferHTML ignored")
	}
	stub := "View this email in your browser: https://example.com/web"
	if m := mustRender(t, multipartAlt(stub, newsletterHTML), Options{}); m.Source != SourceHTML {
		t.Errorf("stub text part used instead of the HTML: %q", m.Text)
	}
}

func TestGmailReplyHTML(t *testing.T) {
	html := `<div dir="ltr">Sounds good!</div><br><div class="gmail_quote"><div dir="ltr" class="gmail_attr">` +
		`On Tue, 3 Nov 2026 at 14:02, Bob Example &lt;<a href="mailto:bob@example.com">bob@example.com</a>&gt; wrote:<br></div>` +
		`<blockquote class="gmail_quote" style="margin:0px 0px 0px 0.8ex"><div>Coffee on Thursday?</div>` +
		`<div><br></div><blockquote><div>Earlier message</div></blockquote></blockquote></div>`
	m := mustRender(t, raw([]string{"Content-Type: text/html; charset=utf-8"}, html), Options{})
	want := "Sounds good!\n\nOn Tue, 3 Nov 2026 at 14:02, Bob Example <bob@example.com [1]> wrote:\n\n> Coffee on Thursday?\n>\n>> Earlier message"
	if m.Text != want {
		t.Errorf("gmail reply:\n%q\nwant\n%q", m.Text, want)
	}
	if len(m.Quotes) != 1 {
		t.Fatalf("quotes = %+v", m.Quotes)
	}
	q := m.Quotes[0]
	if !q.Trailing || q.Who != "Bob Example" || q.Lines != 3 || !strings.HasPrefix(m.Text[q.Start:], "On Tue") {
		t.Errorf("quote = %+v", q)
	}
}

func TestOutlookReplyHTML(t *testing.T) {
	html := `<div>Thanks, will do.</div><div id="appendonsend"></div><hr style="display:inline-block;width:98%">` +
		`<div id="divRplyFwdMsg" dir="ltr"><b>From:</b> Bob Example &lt;bob@example.com&gt;<br><b>Sent:</b> Tuesday, November 3, 2026 2:02 PM<br>` +
		`<b>Subject:</b> Report</div><div>Please send the report.</div>`
	m := mustRender(t, raw([]string{"Content-Type: text/html; charset=utf-8"}, html), Options{})
	if !strings.HasPrefix(m.Text, "Thanks, will do.\n\n> From: Bob Example") {
		t.Errorf("outlook:\n%s", m.Text)
	}
	if len(m.Quotes) != 1 || !m.Quotes[0].Trailing || !strings.HasSuffix(m.Text, "> Please send the report.") {
		t.Errorf("quotes = %+v\n%s", m.Quotes, m.Text)
	}
}

func TestPlainQuotes(t *testing.T) {
	cases := []struct {
		name, text, who string
		trailing        bool
		attribution     bool
	}{
		{"english", "Sure.\n\nOn Tue, 3 Nov 2026 at 14:02, Bob Example <bob@example.com> wrote:\n> Coffee?\n> Thursday?\n", "Bob Example", true, true},
		{"wrapped", "Sure.\n\nOn Tue, 3 Nov 2026 at 14:02, Bob Example <\nbob@example.com> wrote:\n\n> Coffee?\n", "Bob Example", true, true},
		{"finnish", "Käy.\n\nti 3.11.2026 klo 14.02 Liisa Mäkelä (liisa@example.com) kirjoitti:\n> Kahville?\n", "Liisa Mäkelä", true, true},
		{"swedish", "Ja.\n\nDen tis 3 nov. 2026 kl 14:02 skrev Anna Söderström <anna@example.se>:\n> Fika?\n", "Anna Söderström", true, true},
		{"no attribution", "Yes.\n\n> Question\n", "", true, false},
		{"inline answers", "> First question?\nAnswer one.\n\n> Second question?\nAnswer two.\n", "", false, false},
	}
	for _, c := range cases {
		qs := findQuotes(tidy(c.text))
		if len(qs) == 0 {
			t.Errorf("%s: no quote found", c.name)
			continue
		}
		q := qs[0]
		if q.Who != c.who || q.Trailing != c.trailing || (q.Attribution != "") != c.attribution {
			t.Errorf("%s: %+v", c.name, q)
		}
	}
}

func TestInlineAnswersAreNotTrailing(t *testing.T) {
	text := "> First question?\nAnswer one.\n\n> Second question?\nAnswer two."
	qs := findQuotes(text)
	if len(qs) != 2 || qs[0].Trailing || qs[1].Trailing {
		t.Errorf("quotes = %+v", qs)
	}
	if text[qs[1].Start:qs[1].End] != "> Second question?" {
		t.Errorf("second region = %q", text[qs[1].Start:qs[1].End])
	}
}

func TestOutlookPlainText(t *testing.T) {
	text := "Thanks!\n\n________________________________\nFrom: Bob Example <bob@example.com>\nSent: Tuesday, November 3, 2026 14:02\n" +
		"To: Niklas\nSubject: Report\n\nPlease send it."
	qs := findQuotes(text)
	if len(qs) != 1 || !qs[0].Trailing || qs[0].Who != "Bob Example" || qs[0].End != len(text) {
		t.Errorf("quotes = %+v", qs)
	}
	// "From:" alone in prose is not a quote.
	if qs := findQuotes("From: the start of the year we changed plans.\nOK?"); len(qs) != 0 {
		t.Errorf("false positive: %+v", qs)
	}
}

func TestAttachments(t *testing.T) {
	pdf := []byte("%PDF-1.4 fake")
	png := []byte("\x89PNG fake")
	msg := raw([]string{
		"Subject: Files",
		`Content-Type: multipart/mixed; boundary="m"`,
	}, "--m\n"+
		`Content-Type: multipart/related; boundary="r"`+"\n\n"+
		"--r\nContent-Type: text/html; charset=utf-8\n\n<p>See <img src=\"cid:logo@x\" alt=\"logo\"></p>\n"+
		"--r\nContent-Type: image/png\nContent-ID: <logo@x>\nContent-Transfer-Encoding: base64\n\n"+base64.StdEncoding.EncodeToString(png)+"\n"+
		"--r--\n"+
		"--m\nContent-Type: application/pdf\nContent-Disposition: attachment; filename*=utf-8''Lasku%20marraskuu%20%C3%A4.pdf\n"+
		"Content-Transfer-Encoding: base64\n\n"+base64.StdEncoding.EncodeToString(pdf)+"\n"+
		"--m\nContent-Type: text/plain; name=\"notes.txt\"\nContent-Disposition: attachment\n\nnot the body\n"+
		"--m--\n")
	m := mustRender(t, msg, Options{})
	if m.Text != "See [logo]" {
		t.Errorf("text = %q", m.Text)
	}
	if len(m.Attachments) != 3 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	img, doc, txt := m.Attachments[0], m.Attachments[1], m.Attachments[2]
	if !img.Inline || img.ContentID != "logo@x" || img.ContentType != "image/png" || img.Size != len(png) {
		t.Errorf("inline image = %+v", img)
	}
	if doc.Filename != "Lasku marraskuu ä.pdf" || doc.Inline || doc.Size != len(pdf) {
		t.Errorf("pdf = %+v", doc)
	}
	if txt.Filename != "notes.txt" {
		t.Errorf("text attachment = %+v", txt)
	}
	data, err := AttachmentData(msg, doc.Index)
	if err != nil || !bytes.Equal(data, pdf) {
		t.Errorf("AttachmentData = %q, %v", data, err)
	}
	if _, err := AttachmentData(msg, 9); err == nil {
		t.Error("AttachmentData past the end should fail")
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"report.pdf":            "report.pdf",
		"../../.bashrc":         "bashrc",
		`C:\Users\x\evil.exe`:   "evil.exe",
		"a\x00b\nc.txt":         "abc.txt",
		"...":                   "attachment",
		"":                      "attachment",
		"Lasku marraskuu ä.pdf": "Lasku marraskuu ä.pdf",
	}
	for in, want := range cases {
		if got := SafeFilename(in); got != want {
			t.Errorf("SafeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNoBody(t *testing.T) {
	msg := raw([]string{`Content-Type: multipart/mixed; boundary="m"`},
		"--m\nContent-Type: application/zip\nContent-Disposition: attachment; filename=a.zip\n\nPK\n--m--\n")
	m := mustRender(t, msg, Options{})
	if m.Source != SourceNone || m.Text != "" || len(m.Attachments) != 1 {
		t.Errorf("%+v", m)
	}
}

func TestLinkOffset(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/html"},
		`<a href="https://a.example/">a</a> <a href="https://b.example/">b</a> <a href="https://a.example/">a</a>`),
		Options{LinkOffset: 4})
	if m.Text != "a [5] b [6] a [5]" || len(m.Links) != 2 || m.Links[0].N != 5 || m.Links[1].N != 6 {
		t.Errorf("text %q, links %+v", m.Text, m.Links)
	}
}

func TestReplyHeaders(t *testing.T) {
	m := mustRender(t, raw([]string{
		"From: Bob <bob@example.com>",
		"Reply-To: list@example.com",
		"Message-ID: <c@x>",
		"References: <a@x>\r\n <b@x>",
		"Content-Type: text/plain",
	}, "Hi"), Options{})
	if m.MessageID != "c@x" || len(m.References) != 2 || m.References[1] != "b@x" || m.ReplyTo[0].Addr != "list@example.com" {
		t.Errorf("MessageID %q, References %v, ReplyTo %v", m.MessageID, m.References, m.ReplyTo)
	}
}

func TestLenientMessageIDs(t *testing.T) {
	m := mustRender(t, raw([]string{"Message-ID: <abc123>", "References: <one> <two@x>", "Content-Type: text/plain"}, "x"), Options{})
	if m.MessageID != "abc123" || len(m.References) != 2 || m.References[0] != "one" {
		t.Errorf("MessageID %q, References %v", m.MessageID, m.References)
	}
}

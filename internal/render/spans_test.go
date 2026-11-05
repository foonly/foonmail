package render

import (
	"strings"
	"testing"
)

func spanTexts(m *Message) []string {
	var out []string
	for _, s := range m.LinkSpans {
		out = append(out, m.Text[s.Start:s.End])
	}
	return out
}

func TestPlainLinks(t *testing.T) {
	long := "https://news.example.com/track/click?id=0123456789abcdef&campaign=autumn&utm_source=mail"
	body := strings.Join([]string{
		"See https://example.org/page.",
		"Long: " + long,
		"(also www.example.net/x) and <https://example.org/page>",
		"> quoted https://quoted.example/",
		"Not a link: example.com, ftp:// or https:",
	}, "\n")
	m := mustRender(t, raw([]string{"Content-Type: text/plain"}, body), Options{LinkPlain: true, LinkOffset: 2})

	want := strings.Join([]string{
		"See https://example.org/page [3].",
		"Long: " + ShortURL(long, shownURLLength) + " [4]",
		"(also www.example.net/x [5]) and https://example.org/page [3]",
		"> quoted https://quoted.example/ [6]",
		"Not a link: example.com, ftp:// or https:",
	}, "\n")
	if m.Text != want {
		t.Errorf("text:\n got %q\nwant %q", m.Text, want)
	}
	urls := []string{"https://example.org/page", long, "https://www.example.net/x", "https://quoted.example/"}
	if len(m.Links) != len(urls) {
		t.Fatalf("links %+v", m.Links)
	}
	for i, l := range m.Links {
		if l.N != i+3 || l.URL != urls[i] {
			t.Errorf("link %d = %+v, want [%d] %s", i, l, i+3, urls[i])
		}
	}
	spans := spanTexts(m)
	wantSpans := []string{
		"https://example.org/page [3]",
		ShortURL(long, shownURLLength) + " [4]",
		"www.example.net/x [5]",
		"https://example.org/page [3]",
		"https://quoted.example/ [6]",
	}
	if strings.Join(spans, "|") != strings.Join(wantSpans, "|") {
		t.Errorf("spans:\n got %q\nwant %q", spans, wantSpans)
	}
	if len(m.Quotes) != 1 {
		t.Errorf("quotes %+v", m.Quotes)
	}
}

func TestPlainLinksOnlyWhenAsked(t *testing.T) {
	body := "Go to https://example.org/a-rather-long-address/that/goes/on/and/on/for/a/while"
	m := mustRender(t, raw([]string{"Content-Type: text/plain"}, body), Options{})
	if m.Text != body || len(m.Links) != 0 || len(m.LinkSpans) != 0 {
		t.Errorf("text %q, links %+v, spans %+v", m.Text, m.Links, m.LinkSpans)
	}
}

func TestHTMLLinkSpans(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/html"},
		`<p>Read <a href="https://example.org/">the full story</a> or <a href="#top">go up</a>.</p>`+
			`<blockquote>Old <a href="https://old.example/">link</a></blockquote>`),
		Options{})
	got := spanTexts(m)
	want := []string{"the full story [1]", "link [2]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("text %q\nspans %q, want %q", m.Text, got, want)
	}
	if strings.ContainsAny(m.Text, markStart+markEnd+markCancel) {
		t.Errorf("marks left in %q", m.Text)
	}
}

func TestMarksInMailAreIgnored(t *testing.T) {
	for _, ct := range []string{"text/plain", "text/html"} {
		m := mustRender(t, raw([]string{"Content-Type: " + ct + "; charset=utf-8"},
			"fake"+markStart+"span"+markEnd+" here"), Options{LinkPlain: true})
		if m.Text != "fakespan here" || len(m.LinkSpans) != 0 {
			t.Errorf("%s: text %q, spans %+v", ct, m.Text, m.LinkSpans)
		}
	}
}

func TestTrimURL(t *testing.T) {
	cases := []struct{ in, url, tail string }{
		{"https://x.example/a.", "https://x.example/a", "."},
		{"https://x.example/a_(b)", "https://x.example/a_(b)", ""},
		{"https://x.example/a)", "https://x.example/a", ")"},
		{"https://x.example/a).", "https://x.example/a", ")."},
		{"https://x.example/?q=1&r=2", "https://x.example/?q=1&r=2", ""},
	}
	for _, c := range cases {
		if url, tail := trimURL(c.in); url != c.url || tail != c.tail {
			t.Errorf("trimURL(%q) = %q, %q; want %q, %q", c.in, url, tail, c.url, c.tail)
		}
	}
}

// A conversation's messages share numbers for the same address.
func TestEarlierLinksKeepTheirNumbers(t *testing.T) {
	earlier := []Link{{N: 1, URL: "https://www.example.fi"}, {N: 2, URL: "https://a.example/"}}
	plain := mustRender(t, raw([]string{"Content-Type: text/plain"},
		"New https://b.example/ and old www.example.fi again, www.example.fi"),
		Options{LinkPlain: true, Earlier: earlier})
	if want := "New https://b.example/ [3] and old www.example.fi [1] again, www.example.fi [1]"; plain.Text != want {
		t.Errorf("plain:\n got %q\nwant %q", plain.Text, want)
	}
	if len(plain.Links) != 2 || plain.Links[0] != (Link{3, "https://b.example/"}) || plain.Links[1] != (Link{1, "https://www.example.fi"}) {
		t.Errorf("plain links %+v", plain.Links)
	}

	html := mustRender(t, raw([]string{"Content-Type: text/html"},
		`<a href="https://a.example/">a</a> <a href="https://c.example/">c</a>`),
		Options{Earlier: append(earlier, plain.Links...)})
	if html.Text != "a [2] c [4]" || len(html.Links) != 2 || html.Links[1].N != 4 {
		t.Errorf("html: %q %+v", html.Text, html.Links)
	}
}

func TestBracketedAddressGetsSpace(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/plain"},
		"Skickat från Outlook för Android<https://aka.example/AAb9ysg>\n(<https://x.example/>)"),
		Options{LinkPlain: true})
	want := "Skickat från Outlook för Android https://aka.example/AAb9ysg [1]\n(https://x.example/ [2])"
	if m.Text != want {
		t.Errorf("got %q\nwant %q", m.Text, want)
	}
}

func TestTabsExpand(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/plain"},
		"\tCheers,\nab\tc\n\t"+"https://x.example/\tend"), Options{LinkPlain: true})
	want := "        Cheers,\nab      c\n        https://x.example/ [1]  end"
	if m.Text != want {
		t.Errorf("got  %q\nwant %q", m.Text, want)
	}
	if len(m.LinkSpans) != 1 || m.Text[m.LinkSpans[0].Start:m.LinkSpans[0].End] != "https://x.example/ [1]" {
		t.Errorf("spans %+v", m.LinkSpans)
	}
}

func TestBrokenPlainPrefersHTML(t *testing.T) {
	html := `<p>Hey Kim</p><p>Your trip on the ship is approaching!</p><ul><li>Check-in opens 2 hours before.</li><li>Boarding starts 40 minutes before.</li></ul>`
	cases := map[string]string{
		"entities": "Useful information &#8199;&#847; &#8199;&#847; &#8199;&#847;\n&shy; &shy; &shy;\nHey Kim\nYour trip on the ship is approaching!",
		"glued and indented": "                    Hey KimYour trip on the ship is approaching!\n" +
			"                    Schedule:Check-in opens 2 hours before.Boarding starts 40 minutes before.\n" +
			"                    learn more\n                    Tallink app.Download it now.",
	}
	for name, plain := range cases {
		m := mustRender(t, multipartAlt(plain, html), Options{})
		if m.Source != SourceHTML {
			t.Errorf("%s: used the %v part:\n%s", name, m.Source, m.Text)
		}
	}
	// Ordinary text with a little indentation or an odd entity stays.
	ok := "Hi,\n\n    indented code\nSee https://example.org/?a=1&amp;b=2 for more.\nThanks!"
	if m := mustRender(t, multipartAlt(ok, html), Options{}); m.Source != SourcePlain {
		t.Errorf("ordinary text part replaced by HTML")
	}
}

func TestEntitiesDecodedWithoutHTML(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/plain; charset=utf-8"},
		"Preview &#8199;&#847; &#8199;&#847; &shy; &shy;\n&#8199;&#847; &#8199;&#847;\n\nVaihda titaaniseen leikkuulautaan j&#228;&#228;!"), Options{})
	if want := "Preview\n\nVaihda titaaniseen leikkuulautaan jää!"; m.Text != want {
		t.Errorf("got %q\nwant %q", m.Text, want)
	}
}

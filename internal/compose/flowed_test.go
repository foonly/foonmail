package compose

import (
	"strings"
	"testing"
	"unicode/utf8"

	"foonmail/internal/render"
)

const longPara = "Käydäänkö torstaina kahvilla? Ajattelin että voisimme samalla käydä läpi " +
	"ensi viikon suunnitelmat, koska aikataulu on vähän tiukka ja muutama asia on vielä auki."

func TestEncodeFlowedWrapsParagraphs(t *testing.T) {
	got := encodeFlowed(longPara + "\nShort line.\n\nNext paragraph.")
	lines := strings.Split(got, "\n")
	if len(lines) < 5 {
		t.Fatalf("not wrapped:\n%s", got)
	}
	for i, l := range lines {
		if n := utf8.RuneCountInString(l); n > flowedWidth {
			t.Errorf("line %d is %d characters: %q", i, n, l)
		}
	}
	// Soft breaks end in a space; the typed breaks don't.
	soft := 0
	for _, l := range lines {
		if strings.HasSuffix(l, " ") {
			soft++
		}
	}
	if soft != len(lines)-4 { // the wrapped paragraph's last line, "Short line.", "", "Next paragraph."
		t.Errorf("%d soft breaks in %d lines:\n%s", soft, len(lines), got)
	}
	if !strings.HasSuffix(got, "Short line.\n\nNext paragraph.") {
		t.Errorf("hard breaks changed:\n%s", got)
	}
}

func TestEncodeFlowedSpecialLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"trailing spaces   ", "trailing spaces"},
		{"-- ", "-- "},
		{"From the start", " From the start"},
		{"  indented", "   indented"},
		{"> quoted", "> quoted"},
		{">> deeper", ">> deeper"},
		{">", ">"},
		{">no space", "> no space"},
		{"https://example.com/" + strings.Repeat("x", 100), "https://example.com/" + strings.Repeat("x", 100)},
	}
	for _, c := range cases {
		if got := encodeFlowed(c.in); got != c.want {
			t.Errorf("encodeFlowed(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEncodeFlowedQuotesKeepTheirDepth(t *testing.T) {
	got := encodeFlowed("> " + longPara)
	for _, l := range strings.Split(got, "\n") {
		if !strings.HasPrefix(l, "> ") || strings.HasPrefix(l, ">>") {
			t.Errorf("wrapped quote line lost its depth: %q", l)
		}
	}
}

// The renderer decodes format=flowed, so what is sent must come back as
// it was written.
func TestFlowedRoundTrip(t *testing.T) {
	body := strings.Join([]string{
		"Hei!",
		"",
		longPara,
		"",
		"On Tue, Bob wrote:",
		"> " + longPara,
		">",
		">> An older line that is also rather long and should wrap at its own depth too.",
		"",
		"From now on, a line that starts with From.",
		"  Indented code",
		"https://example.com/" + strings.Repeat("y", 90),
		"-- ",
		"Kim",
	}, "\n")
	d := New(acct())
	d.To = "bob@example.com"
	d.Body = body
	raw, err := Build(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "format=flowed") {
		t.Fatal("not marked format=flowed")
	}
	m, err := render.Render(raw, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != body {
		t.Errorf("round trip changed the text:\n got %q\nwant %q", m.Text, body)
	}
}

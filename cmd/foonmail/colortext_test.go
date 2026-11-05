package main

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"foonmail/internal/render"
)

func spanText(text string, spans []textSpan, kind spanKind) []string {
	var out []string
	for _, s := range spans {
		if s.kind == kind {
			out = append(out, text[s.start:s.end])
		}
	}
	return out
}

func TestBodyViewSpans(t *testing.T) {
	body := "Hi, see https://example.org/x.\n\n-- \nKim\nkim@example.org\n\nOn Tue, Bob wrote:\n> Look at https://old.example/\n>> deeper\n"
	raw := []byte("From: Kim <kim@example.org>\r\nContent-Type: text/plain\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n"))
	m, err := render.Render(raw, render.Options{LinkPlain: true})
	if err != nil {
		t.Fatal(err)
	}

	text, spans := bodyView(m, true)
	if got := spanText(text, spans, spanLink); strings.Join(got, "|") != "https://example.org/x [1]|https://old.example/ [2]" {
		t.Errorf("links %q", got)
	}
	if got := spanText(text, spans, spanMuted); strings.Join(got, "|") != "-- \nKim\nkim@example.org\n\nOn Tue, Bob wrote:\n|On Tue, Bob wrote:" {
		t.Errorf("muted %q", got)
	}
	var depths []int
	for _, s := range spans {
		if s.kind == spanQuote {
			depths = append(depths, s.depth)
		}
	}
	if len(depths) != 2 || depths[0] != 1 || depths[1] != 2 {
		t.Errorf("quote depths %v", depths)
	}

	// Collapsed: the quote's spans go, the placeholder is muted, and the
	// link before it stays where it was.
	text, spans = bodyView(m, false)
	if got := spanText(text, spans, spanLink); strings.Join(got, "|") != "https://example.org/x [1]" {
		t.Errorf("collapsed links %q", got)
	}
	muted := spanText(text, spans, spanMuted)
	if len(muted) == 0 || !strings.HasPrefix(muted[len(muted)-1], "[… quoted text from Bob") {
		t.Errorf("collapsed muted %q", muted)
	}
	if got := spanText(text, spans, spanQuote); len(got) != 0 {
		t.Errorf("collapsed quotes still colored: %q", got)
	}
}

func TestQuoteDepth(t *testing.T) {
	for line, want := range map[string]int{"> a": 1, ">> a": 2, "> > a": 2, ">": 1, " > a": 0, "a > b": 0, "": 0} {
		if got := quoteDepth(line); got != want {
			t.Errorf("quoteDepth(%q) = %d, want %d", line, got, want)
		}
	}
}

// The colored glyphs must land where the Selectable over them puts its
// text, or selecting would highlight the wrong letters.
func TestColoredTextLinesUp(t *testing.T) {
	const w, h = 400, 80
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()

	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.NoSystemFonts(), text.WithCollection(gofont.Collection()))
	th.Fg = color.NRGBA{A: 0xff}
	u := &ui{th: th}
	red := color.NRGBA{R: 0xff, A: 0xff}
	s := "plain words then LINKLINK and more plain words that wrap onto a second line"
	start := strings.Index(s, "LINKLINK")
	end := start + len("LINKLINK")
	var state widget.Selectable

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Constraints: layout.Constraints{Max: image.Pt(w, h)}, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	paint.Fill(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
	u.coloredText(gtx, &state, font.Font{Typeface: "Go Mono"}, 16, s, []colorSpan{{start, end, red}})
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}

	regions := state.Regions(len([]rune(s[:start])), len([]rune(s[:end])), nil)
	if len(regions) != 1 {
		t.Fatalf("regions %+v", regions)
	}
	box := regions[0].Bounds
	reds, dark := 0, 0
	for y := range h {
		for x := range w {
			c := img.RGBAAt(x, y)
			isRed := c.R > 0xc0 && c.G < 0x60 && c.B < 0x60
			isDark := c.R < 0x60 && c.G < 0x60 && c.B < 0x60
			in := image.Pt(x, y).In(box.Inset(-1))
			switch {
			case isRed && !in:
				t.Fatalf("red at %d,%d, outside the link's region %v", x, y, box)
			case isRed:
				reds++
			case isDark && image.Pt(x, y).In(box.Inset(2)):
				dark++
			}
		}
	}
	if reds < 20 || dark > 0 {
		t.Errorf("%d red pixels, %d dark pixels inside the link's region %v", reds, dark, box)
	}
}

func TestLinksInCollapsedQuotesAreNotListed(t *testing.T) {
	body := "See https://new.example/ and https://both.example/\n\nOn Tue, Bob wrote:\n> Old https://old.example/\n> and https://both.example/\n"
	raw := []byte("From: Kim <kim@example.org>\r\nContent-Type: text/plain\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n"))
	m, err := render.Render(raw, render.Options{LinkPlain: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Links) != 3 {
		t.Fatalf("links %+v", m.Links)
	}
	text, spans := bodyView(m, false)
	shown, hidden := shownLinks(m, text, spans)
	if len(shown) != 2 || shown[0].N != 1 || shown[1].N != 2 || hidden != 1 {
		t.Errorf("collapsed: shown %+v, hidden %d", shown, hidden)
	}
	text, spans = bodyView(m, true)
	if shown, hidden := shownLinks(m, text, spans); len(shown) != 3 || hidden != 0 {
		t.Errorf("expanded: shown %+v, hidden %d", shown, hidden)
	}
}

func TestSplitNumber(t *testing.T) {
	for line, want := range map[string][2]string{
		"[1] www.example.fi":     {"[1] ", "www.example.fi"},
		"#12 Ordningsregler.pdf": {"#12 ", "Ordningsregler.pdf"},
		"nonumber":               {"", "nonumber"},
	} {
		if num, rest := splitNumber(line); num != want[0] || rest != want[1] {
			t.Errorf("splitNumber(%q) = %q, %q", line, num, rest)
		}
	}
}

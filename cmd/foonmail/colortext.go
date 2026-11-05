package main

import (
	"image/color"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/image/math/fixed"
)

// colorSpan is a byte range of text and its color.
type colorSpan struct {
	start, end int
	color      color.NRGBA
}

// coloredText shows selectable text in several colors. Gio's Selectable
// paints its text in one color, so the text is painted here in colors and
// the Selectable is drawn over it with invisible text, to keep selecting
// and copying. Both lay the text out with the same shaper and parameters,
// so the glyphs land in the same places. Later spans take precedence.
func (u *ui) coloredText(gtx layout.Context, state *widget.Selectable, fnt font.Font, size unit.Sp, s string, spans []colorSpan) layout.Dimensions {
	paintColored(gtx, u.th.Shaper, fnt, size, s, u.th.Fg, spans)
	l := material.Label(u.th, size, s)
	l.State, l.Font = state, fnt
	l.Color = color.NRGBA{}
	return l.Layout(gtx)
}

// paintColored paints text laid out as a widget.Selectable with default
// settings would lay it out.
func paintColored(gtx layout.Context, shaper *text.Shaper, fnt font.Font, size unit.Sp, s string, fg color.NRGBA, spans []colorSpan) {
	// The color of each byte: a palette index, 0 for fg.
	palette := []color.NRGBA{fg}
	byteColor := make([]uint8, len(s))
	for _, sp := range spans {
		if sp.start >= sp.end || sp.start < 0 || sp.end > len(s) || len(palette) == 256 {
			continue
		}
		idx := len(palette)
		for i, c := range palette {
			if c == sp.color {
				idx = i
				break
			}
		}
		if idx == len(palette) {
			palette = append(palette, sp.color)
		}
		for i := sp.start; i < sp.end; i++ {
			byteColor[i] = uint8(idx)
		}
	}

	shaper.LayoutString(text.Parameters{
		Font:     fnt,
		PxPerEm:  fixed.I(gtx.Sp(size)),
		MaxWidth: gtx.Constraints.Max.X,
		MinWidth: gtx.Constraints.Min.X,
		Locale:   gtx.Locale,
	}, s)

	var buf [32]text.Glyph
	run := buf[:0]
	var runColor uint8
	var runOff f32.Point
	flush := func() {
		if len(run) == 0 {
			return
		}
		t := op.Affine(f32.AffineId().Offset(runOff)).Push(gtx.Ops)
		outline := clip.Outline{Path: shaper.Shape(run)}.Op().Push(gtx.Ops)
		paint.ColorOp{Color: palette[runColor]}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		outline.Pop()
		if call := shaper.Bitmaps(run); call != (op.CallOp{}) {
			call.Add(gtx.Ops)
		}
		t.Pop()
		run = run[:0]
	}
	pos := 0 // byte offset of the current cluster
	for {
		g, ok := shaper.NextGlyph()
		if !ok {
			break
		}
		var c uint8
		if pos < len(byteColor) {
			c = byteColor[pos]
		}
		if len(run) > 0 && (c != runColor || len(run) == cap(run)) {
			flush()
		}
		if len(run) == 0 {
			runColor = c
			runOff = f32.Point{X: float32(g.X) / 64, Y: float32(g.Y)}
		}
		run = append(run, g)
		// The last glyph of a cluster carries how many runes it covers.
		for n := 0; n < int(g.Runes) && pos < len(s); n++ {
			_, size := utf8.DecodeRuneInString(s[pos:])
			pos += size
		}
		if g.Flags&text.FlagLineBreak != 0 {
			flush()
		}
	}
	flush()
}

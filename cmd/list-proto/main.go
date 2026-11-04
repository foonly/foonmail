// Command list-proto is a throwaway prototype of the foonmail message list.
// It tests Gio with several thousand rows: keyboard navigation with
// mutt-style keys (including key repeat), mouse selection and scrolling,
// live filtering, and opening a message in a one-view-at-a-time layout.
//
// The data is fake. Run with -n to change the number of conversations.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"foonmail/internal/desktop"
)

const (
	listHints = "j/k or ↑/↓ move · PgUp/PgDn page · = and * first/last · Enter or double-click open · " +
		"N toggle new · d/u delete/undelete · / filter · q quit"
	viewHints = "Esc, q or i back · j/k next/previous message · Space and - page · ↑/↓ scroll · " +
		"select text with the mouse, Ctrl+C copies"
)

type mode int

const (
	modeList mode = iota
	modeView
)

type ui struct {
	w   *app.Window
	th  *material.Theme
	now time.Time

	all  []message
	view []int // indices into all, after filtering
	sel  int   // index into view
	mode mode

	list    widget.List
	listTag *int // focus target for list keys
	click   gesture.Click
	rowH    int // px, this frame
	viewH   int // px, height of the list viewport last frame

	search    widget.Editor
	searching bool

	body      widget.Selectable
	bodyText  string
	bodyList  widget.List
	bodyViewH int

	focusReq event.Tag // focus to request after layout
	perf     perfStats

	mono                      font.Font
	muted, line, barBg, selBg color.NRGBA
	selBgBlurred              color.NRGBA
}

// perfStats tracks how long each frame takes to update, lay out and submit.
type perfStats struct {
	avg      float64 // moving average, ms
	worst    float64 // worst in the last few seconds, ms
	worstAt  time.Time
	filterMs float64
}

func (p *perfStats) add(d time.Duration, at time.Time) {
	ms := float64(d.Microseconds()) / 1000
	if p.avg == 0 {
		p.avg = ms
	} else {
		p.avg = 0.9*p.avg + 0.1*ms
	}
	if ms > p.worst || at.Sub(p.worstAt) > 3*time.Second {
		p.worst, p.worstAt = ms, at
	}
}

func main() {
	n := flag.Int("n", 5000, "number of conversations to generate")
	flag.Parse()
	app.ID = "foonmail-list" // Wayland app_id, for Hyprland window rules
	go func() {
		w := new(app.Window)
		w.Option(app.Title("foonmail list prototype"), app.Size(unit.Dp(1000), unit.Dp(700)))
		if err := run(w, *n); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window, n int) error {
	u := newUI(w, n)
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			start := time.Now()
			gtx := app.NewContext(&ops, e)
			u.update(gtx)
			u.layout(gtx)
			e.Frame(gtx.Ops)
			u.perf.add(time.Since(start), start)
		}
	}
}

func newUI(w *app.Window, n int) *ui {
	now := time.Now()
	u := &ui{
		w:        w,
		th:       material.NewTheme(),
		now:      now,
		all:      generate(n, now),
		listTag:  new(int),
		mono:     font.Font{Typeface: "monospace"},
		list:     widget.List{List: layout.List{Axis: layout.Vertical}},
		bodyList: widget.List{List: layout.List{Axis: layout.Vertical}},
	}
	u.th.TextSize = 15
	u.view = make([]int, len(u.all))
	for i := range u.view {
		u.view[i] = i
	}
	u.search.SingleLine, u.search.Submit = true, true
	u.focusReq = u.listTag

	if desktop.PrefersDark() {
		u.th.Palette = material.Palette{
			Bg: rgb(0x1d1f21), Fg: rgb(0xe0e0e0),
			ContrastBg: rgb(0x6ea8fe), ContrastFg: rgb(0x1d1f21),
		}
		u.muted, u.line, u.barBg = rgb(0x9a9a9a), rgb(0x3a3d40), rgb(0x25282b)
		u.selBg, u.selBgBlurred = rgba(0x6ea8fe, 0x50), rgba(0x6ea8fe, 0x28)
	} else {
		u.th.Palette = material.Palette{
			Bg: rgb(0xfafafa), Fg: rgb(0x1f1f1f),
			ContrastBg: rgb(0x2f6fde), ContrastFg: rgb(0xffffff),
		}
		u.muted, u.line, u.barBg = rgb(0x6b6b6b), rgb(0xd8d8d8), rgb(0xf0f0f0)
		u.selBg, u.selBgBlurred = rgba(0x2f6fde, 0x38), rgba(0x2f6fde, 0x1c)
	}
	return u
}

// Events

// update handles window-wide keys. Each mode's own events are handled in
// layout, right before that mode is drawn; see layout for why.
func (u *ui) update(gtx layout.Context) {
	u.rowH = gtx.Dp(30)
	for {
		ev, ok := gtx.Event(key.Filter{Name: "Q", Required: key.ModCtrl})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			u.w.Perform(system.ActionClose)
		}
	}
}

func (u *ui) updateList(gtx layout.Context) {
	t := u.listTag
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: t},
			key.Filter{Focus: t, Name: "J"},
			key.Filter{Focus: t, Name: "K"},
			key.Filter{Focus: t, Name: key.NameDownArrow},
			key.Filter{Focus: t, Name: key.NameUpArrow},
			key.Filter{Focus: t, Name: key.NamePageDown},
			key.Filter{Focus: t, Name: key.NamePageUp},
			key.Filter{Focus: t, Name: key.NameHome},
			key.Filter{Focus: t, Name: key.NameEnd},
			key.Filter{Focus: t, Name: "=", Optional: key.ModShift},
			key.Filter{Focus: t, Name: "*", Optional: key.ModShift},
			key.Filter{Focus: t, Name: key.NameReturn},
			key.Filter{Focus: t, Name: key.NameEnter},
			key.Filter{Focus: t, Name: "N", Required: key.ModShift},
			key.Filter{Focus: t, Name: "D"},
			key.Filter{Focus: t, Name: "U"},
			key.Filter{Focus: t, Name: "/", Optional: key.ModShift},
			key.Filter{Focus: t, Name: key.NameEscape},
			key.Filter{Focus: t, Name: "Q"},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		page := max(1, u.viewH/u.rowH-1)
		switch ke.Name {
		case "J", key.NameDownArrow:
			u.moveTo(u.sel + 1)
		case "K", key.NameUpArrow:
			u.moveTo(u.sel - 1)
		case key.NamePageDown:
			u.moveTo(u.sel + page)
		case key.NamePageUp:
			u.moveTo(u.sel - page)
		case key.NameHome, "=":
			u.moveTo(0)
		case key.NameEnd, "*":
			u.moveTo(len(u.view) - 1)
		case key.NameReturn, key.NameEnter:
			u.open()
		case "N":
			if m := u.current(); m != nil {
				m.unread = !m.unread
			}
		case "D":
			if m := u.current(); m != nil {
				m.deleted = true
				u.moveTo(u.sel + 1) // mutt moves on after deleting
			}
		case "U":
			if m := u.current(); m != nil {
				m.deleted = false
			}
		case "/":
			u.searching = true
			u.focusReq = &u.search
		case key.NameEscape:
			if u.searching {
				u.clearFilter()
			}
		case "Q":
			u.w.Perform(system.ActionClose)
		}
	}

	for {
		ev, ok := u.click.Update(gtx.Source)
		if !ok {
			break
		}
		switch ev.Kind {
		case gesture.KindPress:
			u.focusReq = u.listTag
			if i := u.rowAt(ev.Position.Y); i >= 0 {
				u.sel = i
			}
		case gesture.KindClick:
			if ev.NumClicks >= 2 && u.rowAt(ev.Position.Y) == u.sel {
				u.open()
			}
		}
	}

	if !u.searching {
		return
	}
	for {
		ev, ok := u.search.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.ChangeEvent:
			u.refilter()
		case widget.SubmitEvent:
			u.focusReq = u.listTag
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.search, Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			u.clearFilter()
		}
	}
}

func (u *ui) updateView(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: "Q"},
			key.Filter{Name: "I"},
			key.Filter{Name: "J"},
			key.Filter{Name: "K"},
			key.Filter{Name: key.NameSpace},
			key.Filter{Name: "-"},
			key.Filter{Name: key.NamePageDown},
			key.Filter{Name: key.NamePageUp},
			key.Filter{Name: key.NameDownArrow},
			key.Filter{Name: key.NameUpArrow},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		page := u.bodyViewH * 9 / 10
		switch ke.Name {
		case key.NameEscape, "Q", "I":
			u.mode = modeList
			u.focusReq = u.listTag
		case "J":
			if u.sel < len(u.view)-1 {
				u.moveTo(u.sel + 1)
				u.open()
			}
		case "K":
			if u.sel > 0 {
				u.moveTo(u.sel - 1)
				u.open()
			}
		case key.NameSpace, key.NamePageDown:
			u.scrollBody(page)
		case "-", key.NamePageUp:
			u.scrollBody(-page)
		case key.NameDownArrow:
			u.scrollBody(3 * u.rowH)
		case key.NameUpArrow:
			u.scrollBody(-3 * u.rowH)
		}
	}
}

// Actions

func (u *ui) current() *message {
	if u.sel < 0 || u.sel >= len(u.view) {
		return nil
	}
	return &u.all[u.view[u.sel]]
}

func (u *ui) moveTo(i int) {
	if len(u.view) == 0 {
		return
	}
	u.sel = min(max(i, 0), len(u.view)-1)
	u.ensureVisible()
}

// ensureVisible scrolls the list just enough to show the selected row.
func (u *ui) ensureVisible() {
	if u.rowH == 0 || u.viewH == 0 {
		return
	}
	visible := max(1, u.viewH/u.rowH)
	p := &u.list.Position
	switch {
	case u.sel <= p.First:
		p.First, p.Offset = u.sel, 0
	case u.sel >= p.First+visible:
		p.First, p.Offset = u.sel-visible+1, 0
	}
}

func (u *ui) rowAt(y int) int {
	if u.rowH == 0 {
		return -1
	}
	i := u.list.Position.First + (y+u.list.Position.Offset)/u.rowH
	if i < 0 || i >= len(u.view) {
		return -1
	}
	return i
}

func (u *ui) open() {
	m := u.current()
	if m == nil {
		return
	}
	m.unread = false
	u.bodyText = fakeBody(*m)
	u.bodyList.Position = layout.Position{}
	u.mode = modeView
}

func (u *ui) scrollBody(px int) {
	p := &u.bodyList.Position
	p.Offset = max(0, p.Offset+px)
}

func (u *ui) refilter() {
	start := time.Now()
	keep := -1
	if m := u.current(); m != nil {
		keep = m.id
	}
	words := strings.Fields(strings.ToLower(u.search.Text()))
	u.view = u.view[:0]
	for i := range u.all {
		if matchesAll(u.all[i].search, words) {
			u.view = append(u.view, i)
		}
	}
	u.sel = 0
	for vi, id := range u.view {
		if id == keep {
			u.sel = vi
			break
		}
	}
	u.list.Position = layout.Position{}
	u.ensureVisible()
	u.perf.filterMs = float64(time.Since(start).Microseconds()) / 1000
}

func matchesAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

func (u *ui) clearFilter() {
	u.search.SetText("")
	u.searching = false
	u.refilter()
	u.focusReq = u.listTag
}

// Layout

func (u *ui) layout(gtx layout.Context) {
	paint.Fill(gtx.Ops, u.th.Bg)
	// Events are handled for the mode that ends up on screen in this frame.
	// Gio drops focus at the end of a frame from any tag that didn't register
	// a FocusFilter during it, so when Esc in the view switches back to the
	// list, the list's filters must be registered in that same frame or the
	// focus request for the list is lost.
	if u.mode == modeView {
		u.updateView(gtx)
	}
	if u.mode == modeList {
		u.updateList(gtx)
	}
	// updateList may have opened a message.
	if u.mode == modeList {
		u.layoutList(gtx)
	} else {
		u.layoutView(gtx)
	}
	if u.focusReq != nil {
		gtx.Execute(key.FocusCmd{Tag: u.focusReq})
		u.focusReq = nil
	}
}

func (u *ui) layoutList(gtx layout.Context) layout.Dimensions {
	unread := 0
	for i := range u.all {
		if u.all[i].unread {
			unread++
		}
	}
	title := fmt.Sprintf("INBOX · %d conversations · %d unread", len(u.all), unread)
	if u.searching && u.search.Len() > 0 {
		title = fmt.Sprintf("INBOX · %d of %d match", len(u.view), len(u.all))
	}

	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, 16, title)
					l.Font.Weight = font.Bold
					return l.Layout(gtx)
				})
		}),
		layout.Rigid(u.divider),
		layout.Flexed(1, u.listArea),
	}
	if u.searching {
		children = append(children, layout.Rigid(u.divider), layout.Rigid(u.searchBar))
	}
	children = append(children, layout.Rigid(u.statusBar(listHints)))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (u *ui) listArea(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	u.viewH = size.Y
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, u.listTag)
	u.click.Add(gtx.Ops)

	if len(u.view) == 0 {
		layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, u.th.TextSize, "No conversations match.")
			l.Color = u.muted
			return l.Layout(gtx)
		})
		return layout.Dimensions{Size: size}
	}
	material.List(u.th, &u.list).Layout(gtx, len(u.view), u.row)
	return layout.Dimensions{Size: size}
}

func (u *ui) row(gtx layout.Context, i int) layout.Dimensions {
	m := &u.all[u.view[i]]
	size := image.Pt(gtx.Constraints.Max.X, u.rowH)
	gtx.Constraints = layout.Exact(size)
	if i == u.sel {
		bg := u.selBgBlurred
		if gtx.Focused(u.listTag) {
			bg = u.selBg
		}
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: size}.Op())
	}

	fg := u.th.Fg
	if m.deleted {
		fg = u.muted
	}
	var f font.Font
	if m.unread {
		f.Weight = font.Bold
	}
	flags := ""
	if m.unread {
		flags += "N"
	}
	if m.deleted {
		flags += "D"
	}
	count := ""
	if m.count > 1 {
		count = fmt.Sprintf("(%d)", m.count)
	}

	return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(u.cell(36, flags, font.Font{Weight: font.Bold}, u.th.ContrastBg)),
			layout.Rigid(u.cell(96, shortDate(m.date, u.now), font.Font{}, u.muted)),
			layout.Rigid(u.cell(230, m.from, f, fg)),
			layout.Rigid(u.cell(52, count, font.Font{}, u.muted)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, m.subject)
				l.MaxLines, l.Font, l.Color = 1, f, fg
				return l.Layout(gtx)
			}),
		)
	})
}

// cell lays out a single-line label in a fixed-width column.
func (u *ui) cell(widthDp unit.Dp, text string, f font.Font, c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		w := min(gtx.Dp(widthDp), gtx.Constraints.Max.X)
		gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
		inner := gtx
		inner.Constraints.Max.X = max(0, w-gtx.Dp(8))
		inner.Constraints.Min.X = 0
		l := material.Label(u.th, u.th.TextSize, text)
		l.MaxLines, l.Font, l.Color = 1, f, c
		d := l.Layout(inner)
		d.Size.X = w
		return d
	}
}

func (u *ui) searchBar(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, "Filter: ")
				l.Color = u.muted
				return l.Layout(gtx)
			}),
			layout.Flexed(1, material.Editor(u.th, &u.search, "words in sender or subject · Enter keeps the filter · Esc clears").Layout),
		)
	})
}

func (u *ui) layoutView(gtx layout.Context) layout.Dimensions {
	m := u.current()
	if m == nil {
		u.mode = modeList
		return layout.Dimensions{}
	}
	conv := ""
	if m.count > 1 {
		conv = fmt.Sprintf(" · %d messages in conversation", m.count)
	}
	header := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, 18, m.subject)
					l.Font.Weight = font.Bold
					return layout.Inset{Bottom: 6}.Layout(gtx, l.Layout)
				}),
				layout.Rigid(material.Label(u.th, u.th.TextSize, "From: "+m.from).Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, u.th.TextSize, m.date.Format("Mon, 2 Jan 2006 15:04")+conv)
					l.Color = u.muted
					return l.Layout(gtx)
				}),
			)
		})
	}
	body := func(gtx layout.Context) layout.Dimensions {
		u.bodyViewH = gtx.Constraints.Max.Y
		return material.List(u.th, &u.bodyList).Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
			return layout.UniformInset(16).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, u.bodyText)
				l.State, l.Font = &u.body, u.mono
				return l.Layout(gtx)
			})
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(header),
		layout.Rigid(u.divider),
		layout.Flexed(1, body),
		layout.Rigid(u.statusBar(viewHints)),
	)
}

func (u *ui) divider(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
	paint.FillShape(gtx.Ops, u.line, clip.Rect{Max: size}.Op())
	return layout.Dimensions{Size: size}
}

func (u *ui) statusBar(hints string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		perf := fmt.Sprintf("frame %.2f ms avg · %.2f ms worst (3 s)", u.perf.avg, u.perf.worst)
		if u.perf.filterMs > 0 {
			perf += fmt.Sprintf(" · last filter %.2f ms", u.perf.filterMs)
		}
		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, u.th.TextSize*0.85, perf)
					return layout.Inset{Bottom: 4}.Layout(gtx, l.Layout)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, u.th.TextSize*0.8, hints)
					l.Color = u.muted
					return l.Layout(gtx)
				}),
			)
		})
		call := macro.Stop()
		paint.FillShape(gtx.Ops, u.barBg, clip.Rect{Max: dims.Size}.Op())
		call.Add(gtx.Ops)
		return dims
	}
}

func rgb(c uint32) color.NRGBA { return rgba(c, 0xff) }

func rgba(c uint32, a uint8) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: a}
}

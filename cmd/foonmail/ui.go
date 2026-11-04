package main

import (
	"fmt"
	"image"
	"image/color"
	"strings"

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

	"foonmail/internal/render"
)

const (
	listHints = "j/k move · PgUp/PgDn page · = and * first/last · Enter open · N read/unread · / filter · q quit"
	viewHints = "Esc, q or i back · j/k next/previous · Space and - page · h quoted text · number + Enter opens a link"
)

// layout handles each mode's events right before drawing it: Gio drops
// focus at the end of a frame from any tag that didn't register a
// FocusFilter during that frame, so the mode that ends up on screen must
// handle its events in the same frame (see the list prototype's test).
func (u *ui) layout(gtx layout.Context) {
	u.rowH = gtx.Dp(30)
	u.globalKeys(gtx)
	paint.Fill(gtx.Ops, u.th.Bg)
	if u.mode == modeView {
		u.updateView(gtx)
	}
	if u.mode == modeList {
		u.updateList(gtx)
	}
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

func (u *ui) globalKeys(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(key.Filter{Name: "Q", Required: key.ModCtrl})
		if !ok {
			return
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press && u.w != nil {
			u.w.Perform(system.ActionClose)
		}
	}
}

// List

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
			if s := u.current(); s != nil {
				u.setSeen(s.UID, unread(*s))
			}
		case "/":
			u.searching = true
			u.focusReq = &u.search
		case key.NameEscape:
			if u.searching {
				u.clearFilter()
			}
		case "Q":
			if u.w != nil {
				u.w.Perform(system.ActionClose)
			}
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
			keep := uint32(0)
			if s := u.current(); s != nil {
				keep = s.UID
			}
			u.applyFilter(keep)
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

func (u *ui) clearFilter() {
	keep := uint32(0)
	if s := u.current(); s != nil {
		keep = s.UID
	}
	u.search.SetText("")
	u.searching = false
	u.applyFilter(keep)
	u.focusReq = u.listTag
}

func (u *ui) layoutList(gtx layout.Context) layout.Dimensions {
	children := []layout.FlexChild{
		layout.Rigid(u.header),
		layout.Rigid(u.divider),
		layout.Flexed(1, u.listArea),
	}
	if u.searching {
		children = append(children, layout.Rigid(u.divider), layout.Rigid(u.searchBar))
	}
	children = append(children, layout.Rigid(u.statusBar(listHints)))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (u *ui) header(gtx layout.Context) layout.Dimensions {
	unreadCount := 0
	for _, it := range u.items {
		if unread(it) {
			unreadCount++
		}
	}
	parts := []string{u.account.Name, u.folder.Name}
	if u.searching && u.search.Len() > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d match", len(u.view), len(u.items)))
	} else {
		parts = append(parts, fmt.Sprintf("%d messages", len(u.items)), fmt.Sprintf("%d unread", unreadCount))
	}
	switch {
	case u.daemon == nil || u.down:
		parts = append(parts, "foonmaild not running: cached mail only")
	case u.status != nil && !u.status.Online:
		parts = append(parts, "offline")
	}
	return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.th, 16, strings.Join(parts, " · "))
		l.Font.Weight = font.Bold
		return l.Layout(gtx)
	})
}

func (u *ui) listArea(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	u.viewH = size.Y
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, u.listTag)
	u.click.Add(gtx.Ops)
	if len(u.view) == 0 {
		msg := "This folder is empty."
		if u.searching && u.search.Len() > 0 {
			msg = "No messages match."
		}
		layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, u.th.TextSize, msg)
			l.Color = u.muted
			return l.Layout(gtx)
		})
		return layout.Dimensions{Size: size}
	}
	material.List(u.th, &u.list).Layout(gtx, len(u.view), u.row)
	return layout.Dimensions{Size: size}
}

func (u *ui) row(gtx layout.Context, i int) layout.Dimensions {
	s := u.items[u.view[i]]
	size := image.Pt(gtx.Constraints.Max.X, u.rowH)
	gtx.Constraints = layout.Exact(size)
	if i == u.sel {
		bg := u.selBgBlurred
		if gtx.Focused(u.listTag) {
			bg = u.selBg
		}
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: size}.Op())
	}
	var f font.Font
	if unread(s) {
		f.Weight = font.Bold
	}
	flags := ""
	if unread(s) {
		flags += "N"
	}
	if flagged(s) {
		flags += "!"
	}
	subject := s.Subject
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(u.cell(36, flags, font.Font{Weight: font.Bold}, u.accent)),
			layout.Rigid(u.cell(96, shortDate(s.Date, u.now()), font.Font{}, u.muted)),
			layout.Rigid(u.cell(230, senderName(s.From), f, u.th.Fg)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, subject)
				l.MaxLines, l.Font = 1, f
				return l.Layout(gtx)
			}),
		)
	})
}

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
			layout.Flexed(1, material.Editor(u.th, &u.search, "sender, recipient or subject · Enter keeps it · Esc clears").Layout),
		)
	})
}

// Message view

func (u *ui) updateView(gtx layout.Context) {
	m := u.msg
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: "Q"},
			key.Filter{Name: "I"},
			key.Filter{Name: "J"},
			key.Filter{Name: "K"},
			key.Filter{Name: "H"},
			key.Filter{Name: key.NameSpace},
			key.Filter{Name: "-"},
			key.Filter{Name: key.NamePageDown},
			key.Filter{Name: key.NamePageUp},
			key.Filter{Name: key.NameDownArrow},
			key.Filter{Name: key.NameUpArrow},
			key.Filter{Name: key.NameReturn},
			key.Filter{Name: key.NameEnter},
			key.Filter{Name: key.NameDeleteBackward},
			key.Filter{Name: "0"}, key.Filter{Name: "1"}, key.Filter{Name: "2"}, key.Filter{Name: "3"},
			key.Filter{Name: "4"}, key.Filter{Name: "5"}, key.Filter{Name: "6"}, key.Filter{Name: "7"},
			key.Filter{Name: "8"}, key.Filter{Name: "9"},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		page := u.viewH * 9 / 10
		switch name := ke.Name; {
		case len(name) == 1 && name[0] >= '0' && name[0] <= '9':
			if len(m.linkInput) < 4 {
				m.linkInput += string(name)
			}
		case name == key.NameDeleteBackward && m.linkInput != "":
			m.linkInput = m.linkInput[:len(m.linkInput)-1]
		case (name == key.NameReturn || name == key.NameEnter) && m.linkInput != "":
			u.openLink(m)
		case name == key.NameEscape && m.linkInput != "":
			m.linkInput = ""
		case name == key.NameEscape || name == "Q" || name == "I":
			u.mode = modeList
			u.focusReq = u.listTag
		case name == "J":
			if u.sel < len(u.view)-1 {
				u.moveTo(u.sel + 1)
				u.open()
			}
		case name == "K":
			if u.sel > 0 {
				u.moveTo(u.sel - 1)
				u.open()
			}
		case name == "H":
			m.showQuotes = !m.showQuotes
		case name == key.NameSpace || name == key.NamePageDown:
			u.scrollBody(page)
		case name == "-" || name == key.NamePageUp:
			u.scrollBody(-page)
		case name == key.NameDownArrow:
			u.scrollBody(3 * u.rowH)
		case name == key.NameUpArrow:
			u.scrollBody(-3 * u.rowH)
		}
	}
}

func (u *ui) scrollBody(px int) {
	if u.msg == nil {
		return
	}
	p := &u.msg.list.Position
	p.Offset = max(0, p.Offset+px)
	if px < 0 && p.Offset == 0 && p.First > 0 {
		p.First--
	}
}

func (u *ui) layoutView(gtx layout.Context) layout.Dimensions {
	m := u.msg
	if m == nil {
		u.mode = modeList
		return layout.Dimensions{}
	}
	hints := viewHints
	if m.linkInput != "" {
		hints = fmt.Sprintf("Open link [%s] — Enter opens, Esc cancels", m.linkInput)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.messageHeader),
		layout.Rigid(u.divider),
		layout.Flexed(1, u.messageBody),
		layout.Rigid(u.statusBar(hints)),
	)
}

func (u *ui) messageHeader(gtx layout.Context) layout.Dimensions {
	m := u.msg
	subject, from, rest := m.sum.Subject, m.sum.From.String(), []string{}
	date := m.sum.Date
	if r := m.rendered; r != nil {
		if r.Subject != "" {
			subject = r.Subject
		}
		if len(r.From) > 0 {
			from = addressList(r.From)
		}
		if len(r.To) > 0 {
			rest = append(rest, "To: "+addressList(r.To))
		}
		if len(r.Cc) > 0 {
			rest = append(rest, "Cc: "+addressList(r.Cc))
		}
		if !r.Date.IsZero() {
			date = r.Date
		}
	}
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	lines := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, 18, subject)
			l.Font.Weight = font.Bold
			return layout.Inset{Bottom: 6}.Layout(gtx, l.Layout)
		}),
		layout.Rigid(material.Label(u.th, u.th.TextSize, "From: "+from).Layout),
	}
	for _, line := range rest {
		lines = append(lines, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, u.th.TextSize, line)
			l.MaxLines = 2
			return l.Layout(gtx)
		}))
	}
	lines = append(lines, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		l := material.Label(u.th, u.th.TextSize, date.Local().Format("Mon, 2 Jan 2006 15:04"))
		l.Color = u.muted
		return l.Layout(gtx)
	}))
	return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, lines...)
	})
}

func (u *ui) messageBody(gtx layout.Context) layout.Dimensions {
	m := u.msg
	u.viewH = gtx.Constraints.Max.Y
	var parts []layout.Widget
	note := func(s string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, u.th.TextSize, s)
			l.Color = u.muted
			return l.Layout(gtx)
		}
	}
	switch {
	case m.loading:
		parts = append(parts, note("Loading…"))
	case m.err != nil:
		parts = append(parts, note("Couldn't show this message: "+m.err.Error()))
	default:
		r := m.rendered
		text := bodyText(r, m.showQuotes)
		if r.Source == render.SourceNone {
			text = "(This message has no text.)"
		}
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, u.th.TextSize, text)
			l.State, l.Font = &m.body, u.mono
			return l.Layout(gtx)
		})
		if r.Source == render.SourceHTML {
			parts = append(parts, note("Converted from HTML."))
		}
		// Room for the link list: about as many characters as fit the width.
		width := max(40, gtx.Constraints.Max.X/max(1, gtx.Sp(u.th.TextSize)*6/10))
		if links := linkLines(r.Links, width); len(links) > 0 {
			parts = append(parts, u.section("Links", links))
		}
		if atts := attachmentLines(r.Attachments); len(atts) > 0 {
			parts = append(parts, u.section("Attachments", atts))
		}
	}
	return material.List(u.th, &m.list).Layout(gtx, len(parts), func(gtx layout.Context, i int) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 4, Left: 16, Right: 16}.Layout(gtx, parts[i])
	})
}

func (u *ui) section(title string, lines []string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(u.divider),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize*0.9, title)
				l.Color, l.Font.Weight = u.muted, font.Bold
				return layout.Inset{Top: 8, Bottom: 4}.Layout(gtx, l.Layout)
			}),
		}
		for _, line := range lines {
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize*0.9, line)
				l.MaxLines, l.Font = 1, u.mono
				return l.Layout(gtx)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
}

// Shared

func (u *ui) divider(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
	paint.FillShape(gtx.Ops, u.line, clip.Rect{Max: size}.Op())
	return layout.Dimensions{Size: size}
}

func (u *ui) statusBar(hints string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if u.notice == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Bottom: 4}.Layout(gtx, material.Label(u.th, u.th.TextSize*0.9, u.notice).Layout)
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

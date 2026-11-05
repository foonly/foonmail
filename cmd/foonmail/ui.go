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
	"gioui.org/io/pointer"
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
	listHints = "j/k move · Enter open · c or ← folders · m new · r reply · g reply all · f forward · a archive · s junk · d trash · N read/unread · / filter"
	viewHints = "Esc back · j/k or →/← conversations · n/p or ↑/↓ messages · r reply · g reply all · f forward · a archive · s junk · d trash · h quoted text · number + Enter opens a link · o/w open/save attachment · b HTML in browser"
)

// layout handles each mode's events right before drawing it: Gio drops
// focus at the end of a frame from any tag that didn't register a
// FocusFilter during that frame, so the mode that ends up on screen must
// handle its events in the same frame (see the list prototype's test).
func (u *ui) layout(gtx layout.Context) {
	u.rowH = gtx.Dp(30)
	u.globalKeys(gtx)
	paint.Fill(gtx.Ops, u.th.Bg)
	// Handle the current mode's events; if that switches mode, handle the
	// new mode's too, so whichever mode this frame ends in has registered
	// its focus filter.
	for range 3 {
		m := u.mode
		switch m {
		case modeList:
			u.updateList(gtx)
			// The pane may have opened just now; its handling must run in this
			// frame too so it keeps the focus it was given.
			if u.pane.open {
				u.updateFolderPane(gtx)
			}
		case modeView:
			u.updateView(gtx)
		case modeCompose:
			u.updateCompose(gtx)
		}
		if u.mode == m {
			break
		}
	}
	switch u.mode {
	case modeList:
		u.layoutList(gtx)
	case modeView:
		u.layoutView(gtx)
	case modeCompose:
		u.layoutCompose(gtx)
	}
	u.releaseTextFocus(gtx)
	if u.focusReq != nil {
		gtx.Execute(key.FocusCmd{Tag: u.focusReq})
		u.focusReq = nil
	}
}

// releaseTextFocus gives the keyboard back to the conversation view when a
// message's text has focus but nothing selected. Clicking into the text
// focuses it (it needs focus for Ctrl+C), and while focused it takes the
// arrow and paging keys. A drag keeps focus, and so does a selection, so
// copying works; the first arrow press clears the selection and the next
// one navigates again.
func (u *ui) releaseTextFocus(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: u.viewPtr, Kinds: pointer.Press | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			u.pressing = pe.Kind == pointer.Press
		}
	}
	if u.mode != modeView || u.conv == nil || u.pressing {
		return
	}
	for _, p := range u.conv.parts {
		if gtx.Focused(&p.body) && p.body.SelectionLen() == 0 {
			gtx.Execute(key.FocusCmd{})
		}
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
			key.Filter{Focus: t, Name: "C"},
			key.Filter{Focus: t, Name: key.NameLeftArrow},
			key.Filter{Focus: t, Name: "/", Optional: key.ModShift},
			key.Filter{Focus: t, Name: "A"},
			key.Filter{Focus: t, Name: "S"},
			key.Filter{Focus: t, Name: "D"},
			key.Filter{Focus: t, Name: key.NameDeleteForward},
			key.Filter{Focus: t, Name: "M"},
			key.Filter{Focus: t, Name: "R"},
			key.Filter{Focus: t, Name: "G"},
			key.Filter{Focus: t, Name: "F"},
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
			u.openSelected()
		case "N":
			u.toggleRead()
		case "C", key.NameLeftArrow:
			u.openPane()
		case "A":
			u.moveConversation(toArchive)
		case "S":
			u.moveConversation(toJunk)
		case "D", key.NameDeleteForward:
			u.moveConversation(toTrash)
		case "M":
			u.newMessage()
		case "R":
			u.replyFromList(kindReply)
		case "G":
			u.replyFromList(kindReplyAll)
		case "F":
			u.replyFromList(kindForward)
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
				u.openSelected()
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
			keep := ""
			if c := u.current(); c != nil {
				keep = c.key
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
	keep := ""
	if c := u.current(); c != nil {
		keep = c.key
	}
	u.search.SetText("")
	u.searching = false
	u.applyFilter(keep)
	u.focusReq = u.listTag
}

func (u *ui) layoutList(gtx layout.Context) layout.Dimensions {
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d := u.header(gtx)
			u.listTop = d.Size.Y + gtx.Dp(1) // the divider below it
			return d
		}),
		layout.Rigid(u.divider),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(u.layoutPane),
				layout.Flexed(1, u.listArea),
			)
		}),
	}
	if u.searching {
		children = append(children, layout.Rigid(u.divider), layout.Rigid(u.searchBar))
	}
	hints := listHints
	if u.pane.open {
		hints = "j/k preview a folder · Enter or → open and sync it · Esc, c or ← back to " + u.pane.origin.Name
	}
	children = append(children, layout.Rigid(u.statusBar(hints)))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (u *ui) header(gtx layout.Context) layout.Dimensions {
	unreadCount := 0
	for _, e := range u.entries {
		if e.FolderID == u.folder.ID && unread(e.Summary) {
			unreadCount++
		}
	}
	parts := []string{u.account.Name, u.folder.Name}
	if u.searching && u.search.Len() > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d conversations match", len(u.view), len(u.convs)))
	} else {
		parts = append(parts, fmt.Sprintf("%d conversations", len(u.convs)), fmt.Sprintf("%d unread", unreadCount))
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
			msg = "No conversations match."
		} else if u.folder.UIDValidity == 0 {
			msg = "This folder hasn't been synced yet."
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
	c := &u.convs[u.view[i]]
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
	if c.unread > 0 {
		f.Weight = font.Bold
	}
	flags := ""
	if c.unread > 0 {
		flags += "N"
	}
	if c.flagged {
		flags += "!"
	}
	count := ""
	if len(c.members) > 1 {
		count = fmt.Sprintf("%d", len(c.members))
	}
	subject := c.subject
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(u.cell(36, flags, font.Font{Weight: font.Bold}, u.accent)),
			layout.Rigid(u.cell(96, shortDate(c.latest, u.now()), font.Font{}, u.muted)),
			layout.Rigid(u.cell(230, c.people, f, u.th.Fg)),
			layout.Rigid(u.cell(36, count, font.Font{}, u.muted)),
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

// Conversation view

func (u *ui) updateView(gtx layout.Context) {
	o := u.conv
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: "Q"},
			key.Filter{Name: "I"},
			key.Filter{Name: "J"},
			key.Filter{Name: "K"},
			key.Filter{Name: "N"},
			key.Filter{Name: "P"},
			key.Filter{Name: "H"},
			key.Filter{Name: "A"},
			key.Filter{Name: "S"},
			key.Filter{Name: "D"},
			key.Filter{Name: key.NameDeleteForward},
			key.Filter{Name: "M"},
			key.Filter{Name: "R"},
			key.Filter{Name: "G"},
			key.Filter{Name: "F"},
			key.Filter{Name: "O"},
			key.Filter{Name: "W"},
			key.Filter{Name: "B"},
			key.Filter{Name: key.NameSpace},
			key.Filter{Name: "-"},
			key.Filter{Name: key.NamePageDown},
			key.Filter{Name: key.NamePageUp},
			key.Filter{Name: key.NameDownArrow},
			key.Filter{Name: key.NameUpArrow},
			key.Filter{Name: key.NameLeftArrow},
			key.Filter{Name: key.NameRightArrow},
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
			if len(o.linkInput) < 4 {
				o.linkInput += string(name)
			}
		case name == key.NameDeleteBackward && o.linkInput != "":
			o.linkInput = o.linkInput[:len(o.linkInput)-1]
		case (name == key.NameReturn || name == key.NameEnter) && o.linkInput != "":
			u.openLink(o)
		case name == key.NameEscape && o.linkInput != "":
			o.linkInput = ""
		case name == key.NameEscape || name == "Q" || name == "I":
			u.mode = modeList
			u.focusReq = u.listTag
		case name == "J" || name == key.NameRightArrow:
			if u.sel < len(u.view)-1 {
				u.moveTo(u.sel + 1)
				u.open()
			}
		case name == "K" || name == key.NameLeftArrow:
			if u.sel > 0 {
				u.moveTo(u.sel - 1)
				u.open()
			}
		case name == "N":
			o.cur = min(o.cur+1, len(o.parts)-1)
			o.list.Position = layout.Position{First: o.cur}
		case name == "P":
			// Back to the top of the current message first, if it is scrolled.
			if o.list.Position.First != o.cur || o.list.Position.Offset == 0 {
				o.cur = max(o.cur-1, 0)
			}
			o.list.Position = layout.Position{First: o.cur}
		case name == "H":
			o.showQuotes = !o.showQuotes
		case name == "A":
			u.moveConversation(toArchive)
		case name == "S":
			u.moveConversation(toJunk)
		case name == "D" || name == key.NameDeleteForward:
			u.moveConversation(toTrash)
		case name == "M":
			u.newMessage()
		case name == "R":
			u.replyFromView(kindReply)
		case name == "O":
			u.attachment(o, false)
		case name == "W":
			u.attachment(o, true)
		case name == "B":
			u.openInBrowser(o)
		case name == "G":
			u.replyFromView(kindReplyAll)
		case name == "F":
			u.replyFromView(kindForward)
		case name == key.NameSpace || name == key.NamePageDown:
			u.scrollBody(page)
		case name == "-" || name == key.NamePageUp:
			u.scrollBody(-page)
		case name == key.NameDownArrow:
			u.arrowDown(o)
		case name == key.NameUpArrow:
			u.arrowUp(o)
		}
	}
}

// arrowDown scrolls the current message while more of it is below the
// view, stopping where it ends; from there it moves to the next message.
// A conversation of one message just scrolls.
func (u *ui) arrowDown(o *openConv) {
	step := 3 * u.rowH
	_, bottom, ok := o.curBounds()
	switch {
	case len(o.parts) < 2 || !ok:
		u.scrollBody(step)
	case bottom > u.viewH:
		u.scrollBody(min(step, bottom-u.viewH))
	case o.cur < len(o.parts)-1:
		o.cur++
		o.list.Position = layout.Position{First: o.cur}
	}
}

// arrowUp scrolls back to the top of the current message, then moves to
// the previous one, showing its end if it doesn't fit in the view.
func (u *ui) arrowUp(o *openConv) {
	step := 3 * u.rowH
	top, _, ok := o.curBounds()
	switch {
	case len(o.parts) < 2 || !ok:
		u.scrollBody(-step)
	case top < 0:
		u.scrollBody(max(-step, top))
	case o.cur > 0:
		o.cur--
		pos := layout.Position{First: o.cur}
		if h, known := o.heights[o.cur]; known && h > u.viewH {
			pos.Offset = h - u.viewH
		}
		o.list.Position = pos
	}
}

// curBounds is where the current message's top and bottom are in the
// view, from the heights drawn last frame.
func (o *openConv) curBounds() (top, bottom int, ok bool) {
	pos := o.list.Position
	if o.cur < pos.First {
		return 0, 0, false
	}
	top = -pos.Offset
	for i := pos.First; i < o.cur; i++ {
		h, known := o.heights[i]
		if !known {
			return 0, 0, false
		}
		top += h
	}
	h, known := o.heights[o.cur]
	return top, top + h, known
}

func (u *ui) scrollBody(px int) {
	if u.conv != nil {
		// The list carries an offset past the first item over to the next
		// items, and a negative one back to earlier items.
		u.conv.list.Position.Offset += px
	}
}

func (u *ui) layoutView(gtx layout.Context) layout.Dimensions {
	o := u.conv
	if o == nil {
		u.mode = modeList
		return layout.Dimensions{}
	}
	hints := viewHints
	if o.linkInput != "" {
		hints = fmt.Sprintf("%s: Enter opens link [%s] · o opens attachment #%s · w saves it · Esc cancels", o.linkInput, o.linkInput, o.linkInput)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.convHeader),
		layout.Rigid(u.divider),
		layout.Flexed(1, u.convBody),
		layout.Rigid(u.statusBar(hints)),
	)
}

func (u *ui) convHeader(gtx layout.Context) layout.Dimensions {
	o := u.conv
	subject := o.conv.subject
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	info := o.conv.people
	if n := len(o.parts); n > 1 {
		info = fmt.Sprintf("%d messages · %s", n, info)
	}
	return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, 18, subject)
				l.Font.Weight = font.Bold
				return layout.Inset{Bottom: 4}.Layout(gtx, l.Layout)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, info)
				l.Color = u.muted
				return l.Layout(gtx)
			}),
		)
	})
}

func (u *ui) convBody(gtx layout.Context) layout.Dimensions {
	o := u.conv
	u.viewH = gtx.Constraints.Max.Y
	// Watch the mouse button without taking events from the text (see
	// releaseTextFocus).
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	defer pointer.PassOp{}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, u.viewPtr)
	width := max(40, gtx.Constraints.Max.X/max(1, gtx.Sp(u.th.TextSize)*6/10))
	dims := material.List(u.th, &o.list).Layout(gtx, len(o.parts), func(gtx layout.Context, i int) layout.Dimensions {
		inset := layout.Inset{Top: 8, Bottom: 8, Left: 16, Right: 16}
		if i != o.cur || len(o.parts) < 2 {
			d := inset.Layout(gtx, u.partWidget(o, o.parts[i], i, width))
			o.heights[i] = d.Size.Y
			return d
		}
		// The current message gets a bar down its left side.
		m := op.Record(gtx.Ops)
		d := inset.Layout(gtx, u.partWidget(o, o.parts[i], i, width))
		call := m.Stop()
		o.heights[i] = d.Size.Y
		top := gtx.Dp(inset.Top)
		if i > 0 {
			top += gtx.Dp(1) // below the divider
		}
		bar := image.Rect(gtx.Dp(5), top, gtx.Dp(8), d.Size.Y-gtx.Dp(inset.Bottom))
		paint.FillShape(gtx.Ops, u.accent, clip.UniformRRect(bar, gtx.Dp(1)).Op(gtx.Ops))
		call.Add(gtx.Ops)
		return d
	})
	// Scrolled out of sight, the current message becomes the top one shown.
	if pos := o.list.Position; o.cur < pos.First || o.cur >= pos.First+pos.Count {
		o.cur = pos.First
	}
	return dims
}

// partWidget draws one message of a conversation: who and when, the body,
// then its links and attachments.
func (u *ui) partWidget(o *openConv, p *part, i, width int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		e := p.entry
		who, addr := senderName(e.From), ""
		if r := p.rendered; r != nil && len(r.From) > 0 {
			who, addr = displayName(r.From[0]), r.From[0].Addr
			if who == addr {
				addr = ""
			}
		}
		nameColor := u.th.Fg
		if i == o.cur && len(o.parts) > 1 {
			nameColor = u.accent
		}
		meta := e.Date.Local().Format("Mon, 2 Jan 2006 15:04")
		sent := e.FolderID != u.folder.ID
		var rows []layout.FlexChild
		if i > 0 {
			rows = append(rows, layout.Rigid(u.divider))
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 8, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								l := material.Label(u.th, u.th.TextSize, who)
								l.Font.Weight, l.MaxLines, l.Color = font.Bold, 1, nameColor
								return l.Layout(gtx)
							}),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								if addr == "" {
									return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
								}
								l := material.Label(u.th, u.th.TextSize*0.9, "  "+addr)
								l.Color, l.MaxLines = u.muted, 1
								return l.Layout(gtx)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if !sent {
									return layout.Dimensions{}
								}
								l := material.Label(u.th, u.th.TextSize*0.9, "sent · ")
								l.Color = u.accent
								return l.Layout(gtx)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								l := material.Label(u.th, u.th.TextSize*0.9, meta)
								l.Color = u.muted
								return l.Layout(gtx)
							}),
						)
					}),
					layout.Rigid(u.recipients(p.rendered)),
				)
			})
		}))
		note := func(s string) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, s)
				l.Color = u.muted
				return l.Layout(gtx)
			})
		}
		switch {
		case p.loading:
			rows = append(rows, note("Loading…"))
		case p.err != nil:
			rows = append(rows, note("Couldn't show this message: "+p.err.Error()))
		default:
			r := p.rendered
			text, spans := bodyView(r, o.showQuotes)
			if r.Source == render.SourceNone {
				text, spans = "(This message has no text.)", nil
			}
			colors := u.spanColors(spans)
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.coloredText(gtx, &p.body, u.mono, u.th.TextSize, text, colors)
			}))
			if r.Source == render.SourceHTML {
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, u.th.TextSize*0.85, "Converted from HTML.")
						l.Color = u.muted
						return l.Layout(gtx)
					})
				}))
			}
			shown, hidden := shownLinks(r, text, spans)
			if links := linkLines(shown, width); len(links) > 0 {
				rows = append(rows, layout.Rigid(u.section("Links", links, true)))
			}
			if hidden > 0 {
				what := fmt.Sprintf("%d more links in the quoted text · h shows them", hidden)
				if hidden == 1 {
					what = "1 more link in the quoted text · h shows it"
				}
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, u.th.TextSize*0.85, what)
					l.Color = u.muted
					return layout.Inset{Top: 2}.Layout(gtx, l.Layout)
				}))
			}
			if atts := attachmentLines(r); len(atts) > 0 {
				rows = append(rows, layout.Rigid(u.section("Attachments", atts, true)))
			}
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	}
}

// section lists lines under a title; numbered lines start with their
// number ("[N]" or "#N") and a space, and the number is colored like the
// links in the text.
func (u *ui) section(title string, lines []string, numbered bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize*0.9, title)
				l.Color, l.Font.Weight = u.muted, font.Bold
				return layout.Inset{Top: 10, Bottom: 4}.Layout(gtx, l.Layout)
			}),
		}
		for _, line := range lines {
			num := ""
			if i := strings.IndexByte(line, ' '); numbered && i > 0 {
				num, line = line[:i+2], line[i+2:]
			}
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, u.th.TextSize*0.9, num)
						l.Font, l.Color = u.mono, u.accent
						return l.Layout(gtx)
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						l := material.Label(u.th, u.th.TextSize*0.9, line)
						l.MaxLines, l.Font = 1, u.mono
						return l.Layout(gtx)
					}))
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

// recipients is the "to … cc …" line under a message's sender.
func (u *ui) recipients(r *render.Message) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if r == nil || len(r.To)+len(r.Cc) == 0 {
			return layout.Dimensions{}
		}
		var parts []layout.FlexChild
		field := func(label string, as []render.Address) {
			if len(as) == 0 {
				return
			}
			names := make([]string, len(as))
			for i, a := range as {
				names[i] = displayName(a)
			}
			prefix := label + " "
			if len(parts) > 0 {
				prefix = "   " + prefix
			}
			parts = append(parts,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, u.th.TextSize*0.85, prefix)
					l.Color = u.muted
					return l.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(u.th, u.th.TextSize*0.85, strings.Join(names, ", "))
					l.MaxLines = 1
					return l.Layout(gtx)
				}))
		}
		field("to", r.To)
		field("cc", r.Cc)
		return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx, parts...)
		})
	}
}

// spanColors gives the shown text's spans their theme colors.
func (u *ui) spanColors(spans []textSpan) []colorSpan {
	out := make([]colorSpan, 0, len(spans))
	for _, s := range spans {
		c := u.muted
		switch s.kind {
		case spanLink:
			c = u.accent
		case spanQuote:
			if len(u.quoteColors) > 0 {
				c = u.quoteColors[(s.depth-1)%len(u.quoteColors)]
			}
		}
		out = append(out, colorSpan{start: s.start, end: s.end, color: c})
	}
	return out
}

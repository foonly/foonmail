package main

import (
	"context"
	"fmt"
	"image"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"foonmail/internal/cache"
)

const (
	paneWidth    unit.Dp = 240
	paneDuration         = 150 * time.Millisecond
)

// folderPane is the folder list that slides in left of the message list.
// Moving through it previews each folder from the cache; Enter or →
// chooses one (and syncs it), Esc, c or ← goes back to where you were.
type folderPane struct {
	open    bool
	changed time.Time // when it last started opening or closing, for the slide
	rows    []folderRow
	sel     int
	origin  cache.Folder // the folder shown when the pane opened
	tag     *int
	list    widget.List
	click   gesture.Click
}

// folderRow is one line of the folder pane.
type folderRow struct {
	folder cache.Folder
	label  string // the last part of the name
	depth  int    // nesting level, for indentation
	counts cache.Counts
}

var specialLabels = map[string]string{
	`\Sent`: "sent", `\Drafts`: "drafts", `\Trash`: "trash", `\Junk`: "junk",
	`\Archive`: "archive", `\All`: "all mail", `\Flagged`: "flagged",
}

// folderRows orders folders: INBOX and its subfolders first, then the rest
// by name, each under its parent.
func folderRows(folders []cache.Folder, counts map[int64]cache.Counts) []folderRow {
	sorted := slices.Clone(folders)
	inInbox := func(f cache.Folder) bool {
		return f.Name == "INBOX" || (f.Delimiter != "" && strings.HasPrefix(f.Name, "INBOX"+f.Delimiter))
	}
	slices.SortStableFunc(sorted, func(a, b cache.Folder) int {
		if ia, ib := inInbox(a), inInbox(b); ia != ib {
			if ia {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	rows := make([]folderRow, len(sorted))
	for i, f := range sorted {
		r := folderRow{folder: f, label: f.Name, counts: counts[f.ID]}
		if f.Delimiter != "" {
			parts := strings.Split(f.Name, f.Delimiter)
			r.label, r.depth = parts[len(parts)-1], len(parts)-1
		}
		rows[i] = r
	}
	return rows
}

// refreshPaneRows rereads the folder list and counts, keeping the selection.
func (u *ui) refreshPaneRows() {
	ctx := context.Background()
	folders, err := u.db.Folders(ctx, u.accountID)
	if err != nil {
		u.notice = "Reading folders failed: " + err.Error()
		return
	}
	counts, err := u.db.FolderCounts(ctx, u.accountID)
	if err != nil {
		u.notice = "Reading folder counts failed: " + err.Error()
	}
	u.pane.rows = folderRows(folders, counts)
	u.pane.sel = 0
	for i, r := range u.pane.rows {
		if r.folder.ID == u.folder.ID {
			u.pane.sel = i
		}
	}
}

func (u *ui) openPane() {
	if u.searching {
		u.clearFilter()
	}
	u.refreshPaneRows()
	p := &u.pane
	p.open, p.changed, p.origin = true, u.now(), u.folder
	p.list.Position = layout.Position{First: max(0, p.sel-5)}
	u.focusReq = p.tag
}

func (u *ui) closePane() {
	u.pane.open, u.pane.changed = false, u.now()
	u.focusReq = u.listTag
}

// previewFolder shows a folder's cached contents without syncing it.
func (u *ui) previewFolder(f cache.Folder) {
	if f.ID == u.folder.ID {
		return
	}
	u.showFolder(f)
}

// chooseFolder keeps the previewed folder, syncs it, and goes back to the
// message list.
func (u *ui) chooseFolder() {
	p := &u.pane
	if p.sel < 0 || p.sel >= len(p.rows) {
		return
	}
	f := p.rows[p.sel].folder
	u.closePane()
	u.showFolder(f)
	u.syncFolder(f)
}

// cancelPane goes back to the folder shown before the pane opened.
func (u *ui) cancelPane() {
	u.closePane()
	if u.folder.ID != u.pane.origin.ID {
		u.showFolder(u.pane.origin)
	}
}

func (u *ui) movePane(i int) {
	p := &u.pane
	if len(p.rows) == 0 {
		return
	}
	p.sel = min(max(i, 0), len(p.rows)-1)
	// Keep the selection on screen.
	visible := max(1, u.viewH/max(1, u.rowH))
	if p.sel < p.list.Position.First {
		p.list.Position.First, p.list.Position.Offset = p.sel, 0
	} else if p.sel >= p.list.Position.First+visible {
		p.list.Position.First, p.list.Position.Offset = p.sel-visible+1, 0
	}
	u.previewFolder(p.rows[p.sel].folder)
}

func (u *ui) updateFolderPane(gtx layout.Context) {
	p := &u.pane
	t := p.tag
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
			key.Filter{Focus: t, Name: key.NameReturn},
			key.Filter{Focus: t, Name: key.NameEnter},
			key.Filter{Focus: t, Name: key.NameRightArrow},
			key.Filter{Focus: t, Name: key.NameLeftArrow},
			key.Filter{Focus: t, Name: key.NameEscape},
			key.Filter{Focus: t, Name: "C"},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press || !p.open {
			continue
		}
		page := max(1, u.viewH/max(1, u.rowH)-1)
		switch ke.Name {
		case "J", key.NameDownArrow:
			u.movePane(p.sel + 1)
		case "K", key.NameUpArrow:
			u.movePane(p.sel - 1)
		case key.NamePageDown:
			u.movePane(p.sel + page)
		case key.NamePageUp:
			u.movePane(p.sel - page)
		case key.NameHome:
			u.movePane(0)
		case key.NameEnd:
			u.movePane(len(p.rows) - 1)
		case key.NameReturn, key.NameEnter, key.NameRightArrow:
			u.chooseFolder()
		case key.NameEscape, "C", key.NameLeftArrow:
			u.cancelPane()
		}
	}
	for {
		ev, ok := p.click.Update(gtx.Source)
		if !ok || !p.open {
			break
		}
		i := p.list.Position.First + (ev.Position.Y+p.list.Position.Offset)/max(1, u.rowH)
		if i < 0 || i >= len(p.rows) {
			continue
		}
		switch {
		case ev.Kind == gesture.KindPress:
			u.focusReq = p.tag
			u.movePane(i)
		case ev.Kind == gesture.KindClick && ev.NumClicks >= 2 && i == p.sel:
			u.chooseFolder()
		}
	}
}

// paneShown is how far the pane is out, 0 to 1, easing as it slides. While
// it moves, the next frame is requested.
func (u *ui) paneShown(gtx layout.Context) float32 {
	p := &u.pane
	t := float32(u.now().Sub(p.changed)) / float32(paneDuration)
	if p.changed.IsZero() || t >= 1 {
		if p.open {
			return 1
		}
		return 0
	}
	gtx.Execute(op.InvalidateCmd{})
	eased := 1 - (1-t)*(1-t)*(1-t) // ease-out cubic
	if p.open {
		return eased
	}
	return 1 - eased
}

// layoutPane draws the pane at its current slide width.
func (u *ui) layoutPane(gtx layout.Context) layout.Dimensions {
	shown := u.paneShown(gtx)
	w := int(float32(gtx.Dp(paneWidth)) * shown)
	if u.pane.open {
		// Register the pane even before it has slid out: Gio drops focus from
		// a handler that has no input area in the frame.
		area := clip.Rect{Max: image.Pt(max(w, 1), gtx.Constraints.Max.Y)}.Push(gtx.Ops)
		event.Op(gtx.Ops, u.pane.tag)
		u.pane.click.Add(gtx.Ops)
		area.Pop()
	}
	if w <= 0 {
		return layout.Dimensions{}
	}
	size := image.Pt(w, gtx.Constraints.Max.Y)
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, u.barBg, clip.Rect{Max: size}.Op())
	// Lay the content out at full width and let the clip reveal it, so the
	// text slides in rather than reflowing.
	inner := gtx
	inner.Constraints = layout.Exact(image.Pt(gtx.Dp(paneWidth), size.Y))
	off := op.Offset(image.Pt(w-gtx.Dp(paneWidth), 0)).Push(gtx.Ops)
	material.List(u.th, &u.pane.list).Layout(inner, len(u.pane.rows), u.paneRow)
	off.Pop()
	return layout.Dimensions{Size: size}
}

func (u *ui) paneRow(gtx layout.Context, i int) layout.Dimensions {
	r := u.pane.rows[i]
	size := image.Pt(gtx.Constraints.Max.X, u.rowH)
	gtx.Constraints = layout.Exact(size)
	if i == u.pane.sel {
		bg := u.selBgBlurred
		if gtx.Focused(u.pane.tag) {
			bg = u.selBg
		}
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: size}.Op())
	}
	var f font.Font
	if r.counts.Unread > 0 {
		f.Weight = font.Bold
	}
	detail := ""
	switch {
	case r.counts.Unread > 0:
		detail = fmt.Sprint(r.counts.Unread)
	case r.counts.Total == 0 && r.folder.UIDValidity == 0:
		detail = "·"
	}
	if s := specialLabels[r.folder.SpecialUse]; s != "" && detail == "" {
		detail = s
	}
	return layout.Inset{Left: unit.Dp(12 + 14*r.depth), Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, r.label)
				l.MaxLines, l.Font = 1, f
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize*0.85, detail)
				l.Color = u.muted
				return l.Layout(gtx)
			}),
		)
	})
}

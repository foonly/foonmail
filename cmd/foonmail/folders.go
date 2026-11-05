package main

import (
	"context"
	"fmt"
	"image"
	"slices"
	"strconv"
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

// folderRow is one line of the folder pane: a folder, or with several
// accounts, an account's name above its folders.
type folderRow struct {
	folder cache.Folder
	label  string // the last part of the name
	depth  int    // nesting level, for indentation
	counts cache.Counts
	header bool // an account's name, not a folder; it can't be selected
}

var specialLabels = map[string]string{
	`\Sent`: "sent", `\Drafts`: "drafts", `\Trash`: "trash", `\Junk`: "junk",
	`\Archive`: "archive", `\All`: "all mail", `\Flagged`: "flagged",
}

// specialOrder is the order of the special folders, after INBOX.
var specialOrder = []string{`\Drafts`, `\Sent`, `\Archive`, `\Junk`, `\Trash`}

// folderRows orders folders for the pane: INBOX and its subfolders, then
// Drafts, Sent, the archive, Junk and Trash, then the rest by name. Each
// folder brings its subfolders along, indented under it.
//
// The archive is the folder flagged \Archive, or else a top-level folder
// named Archive or Archives; if it has a subfolder for this year, that
// subfolder is shown there instead ("Archives 2026"), and the other years
// stay under Archives with the rest.
func folderRows(folders []cache.Folder, counts map[int64]cache.Counts, year int) []folderRow {
	sorted := slices.Clone(folders)
	slices.SortFunc(sorted, func(a, b cache.Folder) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	under := func(child, parent cache.Folder) bool {
		return child.Name == parent.Name ||
			(parent.Delimiter != "" && strings.HasPrefix(child.Name, parent.Name+parent.Delimiter))
	}
	parts := func(f cache.Folder) []string {
		if f.Delimiter == "" {
			return []string{f.Name}
		}
		return strings.Split(f.Name, f.Delimiter)
	}

	special, yearLabel := specialFolders(sorted, year)

	var rows []folderRow
	placed := map[int64]bool{}
	// group appends root and its subfolders, indented relative to root.
	group := func(root cache.Folder, label string) {
		base := len(parts(root)) - 1
		for _, f := range sorted {
			if placed[f.ID] || !under(f, root) {
				continue
			}
			p := parts(f)
			r := folderRow{folder: f, label: p[len(p)-1], depth: len(p) - 1 - base, counts: counts[f.ID]}
			if f.ID == root.ID && label != "" {
				r.label = label
			}
			rows = append(rows, r)
			placed[f.ID] = true
		}
	}
	// Special folders inside INBOX, with their subfolders, go to their own
	// place, not INBOX's.
	subtree := func(root cache.Folder, mark bool) {
		for _, f := range sorted {
			if under(f, root) {
				placed[f.ID] = mark
			}
		}
	}
	for _, s := range specialOrder {
		if f, ok := special[s]; ok && f.Name != "INBOX" {
			subtree(f, true)
		}
	}
	for _, f := range sorted {
		if f.Name == "INBOX" {
			group(f, "")
		}
	}
	for _, s := range specialOrder {
		f, ok := special[s]
		if !ok || f.Name == "INBOX" {
			continue
		}
		subtree(f, false)
		label := ""
		if s == `\Archive` {
			label = yearLabel
		}
		group(f, label)
	}
	for _, f := range sorted {
		if !placed[f.ID] {
			// The top of a remaining tree: depth counts from the top level.
			p := parts(f)
			rows = append(rows, folderRow{folder: f, label: p[len(p)-1], depth: len(p) - 1, counts: counts[f.ID]})
			placed[f.ID] = true
		}
	}
	return rows
}

// refreshPaneRows rereads the folder list and counts, keeping the
// selection. With several accounts, each account's folders come under its
// name, in the order of the configuration.
func (u *ui) refreshPaneRows() {
	if len(u.missing) > 0 {
		u.loadMissing()
	}
	ids := []int64{u.accountID}
	if len(u.accounts) > 1 {
		ids = ids[:0]
		for _, a := range u.accounts {
			ids = append(ids, a.id)
		}
	}
	var rows []folderRow
	for _, id := range ids {
		r, err := u.accountRows(id)
		if err != nil {
			u.notice = err.Error()
			return
		}
		if len(ids) > 1 {
			rows = append(rows, folderRow{label: u.accountByID(id).cfg.Name, header: true, folder: cache.Folder{AccountID: id}})
		}
		rows = append(rows, r...)
	}
	u.pane.rows = rows
	u.pane.sel = u.pane.selectable(0, 1)
	for i, r := range u.pane.rows {
		if !r.header && r.folder.ID == u.folder.ID {
			u.pane.sel = i
		}
	}
}

// accountRows are one account's folders in pane order.
func (u *ui) accountRows(accountID int64) ([]folderRow, error) {
	ctx := context.Background()
	folders, err := u.db.Folders(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("Reading folders failed: %w", err)
	}
	counts, err := u.db.FolderCounts(ctx, accountID)
	if err != nil {
		u.notice = "Reading folder counts failed: " + err.Error()
	}
	return folderRows(folders, counts, u.now().Year()), nil
}

// selectable is the first folder row from i on in direction dir (+1 or
// -1), or the other way if there is none; account names are skipped.
func (p *folderPane) selectable(i, dir int) int {
	if dir == 0 {
		dir = 1
	}
	for _, d := range []int{dir, -dir} {
		for j := min(max(i, 0), len(p.rows)-1); j >= 0 && j < len(p.rows); j += d {
			if !p.rows[j].header {
				return j
			}
		}
	}
	return 0
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
	dir := 1
	if i < p.sel {
		dir = -1
	}
	p.sel = p.selectable(i, dir)
	// Keep the selection on screen, with an account's name above it.
	visible := max(1, u.viewH/max(1, u.rowH))
	top := p.sel
	if top > 0 && p.rows[top-1].header {
		top--
	}
	if top < p.list.Position.First {
		p.list.Position.First, p.list.Position.Offset = top, 0
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
		if i < 0 || i >= len(p.rows) || p.rows[i].header {
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
	if w <= 0 && !u.pane.open {
		return layout.Dimensions{}
	}
	// The area is at least 1 px wide: an open pane must register even before
	// it has slid out, as Gio drops focus from a handler with no input area
	// in the frame. Everything below is drawn inside it, so the list's own
	// pointer handling (scrolling) doesn't hide clicks from the pane.
	size := image.Pt(max(w, 1), gtx.Constraints.Max.Y)
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	if u.pane.open {
		event.Op(gtx.Ops, u.pane.tag)
		u.pane.click.Add(gtx.Ops)
	}
	if w <= 0 {
		return layout.Dimensions{Size: image.Pt(0, size.Y)}
	}
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
	if r.header {
		return u.accountHeader(gtx, r, i)
	}
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

// specialFolders finds Drafts, Sent, the archive, Junk and Trash by their
// special-use flags. The archive may instead be a top-level Archive or
// Archives folder, and is then its subfolder for this year if there is
// one; yearLabel names that ("Archives 2026"). folders must be sorted by
// name.
func specialFolders(folders []cache.Folder, year int) (special map[string]cache.Folder, yearLabel string) {
	special = map[string]cache.Folder{}
	for _, f := range folders {
		if s := f.SpecialUse; slices.Contains(specialOrder, s) {
			if _, taken := special[s]; !taken {
				special[s] = f
			}
		}
	}
	if _, ok := special[`\Archive`]; !ok {
		for _, f := range folders {
			top := f.Delimiter == "" || !strings.Contains(f.Name, f.Delimiter)
			if top && (strings.EqualFold(f.Name, "Archive") || strings.EqualFold(f.Name, "Archives")) {
				special[`\Archive`] = f
				break
			}
		}
	}
	if a, ok := special[`\Archive`]; ok && a.Delimiter != "" {
		thisYear := a.Name + a.Delimiter + strconv.Itoa(year)
		for _, f := range folders {
			if f.Name == thisYear {
				special[`\Archive`] = f
				yearLabel = a.Name + " " + strconv.Itoa(year)
				break
			}
		}
	}
	return special, yearLabel
}

// accountHeader is an account's name above its folders, with a line
// between accounts.
func (u *ui) accountHeader(gtx layout.Context, r folderRow, i int) layout.Dimensions {
	size := gtx.Constraints.Max
	if i > 0 {
		paint.FillShape(gtx.Ops, u.line, clip.Rect{Max: image.Pt(size.X, gtx.Dp(1))}.Op())
	}
	return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize*0.85, strings.ToUpper(r.label))
				l.Color, l.Font.Weight, l.MaxLines = u.accent, font.Bold, 1
				return l.Layout(gtx)
			}),
		)
	})
}

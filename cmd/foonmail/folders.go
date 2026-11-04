package main

import (
	"context"
	"fmt"
	"image"
	"slices"
	"strings"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget/material"

	"foonmail/internal/cache"
)

// folderRow is one line of the folder picker.
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

// folderRows orders folders for the picker: INBOX and its subfolders first,
// then the rest by name, each under its parent.
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

// openFolders shows the folder picker with fresh counts.
func (u *ui) openFolders() {
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
	u.folders = folderRows(folders, counts)
	u.folderSel = 0
	for i, r := range u.folders {
		if r.folder.ID == u.folder.ID {
			u.folderSel = i
		}
	}
	u.folderLst.Position = layout.Position{First: max(0, u.folderSel-5)}
	u.mode = modeFolders
	u.focusReq = u.folderTag
}

func (u *ui) updateFolders(gtx layout.Context) {
	t := u.folderTag
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: t},
			key.Filter{Focus: t, Name: "J"},
			key.Filter{Focus: t, Name: "K"},
			key.Filter{Focus: t, Name: key.NameDownArrow},
			key.Filter{Focus: t, Name: key.NameUpArrow},
			key.Filter{Focus: t, Name: key.NameHome},
			key.Filter{Focus: t, Name: key.NameEnd},
			key.Filter{Focus: t, Name: key.NameReturn},
			key.Filter{Focus: t, Name: key.NameEnter},
			key.Filter{Focus: t, Name: key.NameEscape},
			key.Filter{Focus: t, Name: "C"},
			key.Filter{Focus: t, Name: "Q"},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		last := len(u.folders) - 1
		switch ke.Name {
		case "J", key.NameDownArrow:
			u.folderSel = min(u.folderSel+1, last)
		case "K", key.NameUpArrow:
			u.folderSel = max(u.folderSel-1, 0)
		case key.NameHome:
			u.folderSel = 0
		case key.NameEnd:
			u.folderSel = last
		case key.NameReturn, key.NameEnter:
			if u.folderSel >= 0 && u.folderSel <= last {
				u.switchFolder(u.folders[u.folderSel].folder)
			}
		case key.NameEscape, "C", "Q":
			u.mode = modeList
			u.focusReq = u.listTag
		}
		// Keep the selection on screen.
		p := &u.folderLst.Position
		if u.folderSel < p.First {
			p.First, p.Offset = u.folderSel, 0
		} else if visible := max(1, u.viewH/u.rowH); u.folderSel >= p.First+visible {
			p.First, p.Offset = u.folderSel-visible+1, 0
		}
	}
}

func (u *ui) layoutFolders(gtx layout.Context) layout.Dimensions {
	header := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := material.Label(u.th, 16, u.account.Name+" · folders")
			l.Font.Weight = font.Bold
			return l.Layout(gtx)
		})
	}
	area := func(gtx layout.Context) layout.Dimensions {
		size := gtx.Constraints.Max
		u.viewH = size.Y
		defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, u.folderTag)
		material.List(u.th, &u.folderLst).Layout(gtx, len(u.folders), u.folderRow)
		return layout.Dimensions{Size: size}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(header),
		layout.Rigid(u.divider),
		layout.Flexed(1, area),
		layout.Rigid(u.statusBar("j/k move · Enter opens the folder · Esc or c back")),
	)
}

func (u *ui) folderRow(gtx layout.Context, i int) layout.Dimensions {
	r := u.folders[i]
	size := image.Pt(gtx.Constraints.Max.X, u.rowH)
	gtx.Constraints = layout.Exact(size)
	if i == u.folderSel {
		paint.FillShape(gtx.Ops, u.selBg, clip.Rect{Max: size}.Op())
	}
	var f font.Font
	if r.counts.Unread > 0 {
		f.Weight = font.Bold
	}
	label := strings.Repeat("    ", r.depth) + r.label
	if r.folder.ID == u.folder.ID {
		label += "  ◂"
	}
	detail := ""
	switch {
	case r.counts.Total == 0 && r.folder.UIDValidity == 0:
		detail = "not synced yet"
	case r.counts.Unread > 0:
		detail = fmt.Sprintf("%d unread · %d", r.counts.Unread, r.counts.Total)
	default:
		detail = fmt.Sprint(r.counts.Total)
	}
	if s := specialLabels[r.folder.SpecialUse]; s != "" {
		detail = s + " · " + detail
	}
	return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, label)
				l.MaxLines, l.Font = 1, f
				return l.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize*0.9, detail)
				l.Color = u.muted
				return l.Layout(gtx)
			}),
		)
	})
}

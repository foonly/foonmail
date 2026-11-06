package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"foonmail/internal/cache"
	"foonmail/internal/rpc"
)

// In the folder pane, n asks for a name and creates a folder in the shown
// account, and d deletes the selected folder, with its mail, after a
// second d. Both go through the daemon, which does them on the server.

// startNewFolder opens the name field under the list.
func (u *ui) startNewFolder() {
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: can't create folders"
		return
	}
	p := &u.pane
	p.naming = true
	p.deleting = 0
	p.name.SetText("")
	u.notice = ""
	u.focusReq = &p.name
}

func (u *ui) stopNaming() {
	u.pane.naming = false
	u.focusReq = u.pane.tag
}

// updateNameField handles the name field's Enter and Esc.
func (u *ui) updateNameField(gtx layout.Context) {
	p := &u.pane
	if !p.naming {
		return
	}
	for {
		ev, ok := p.name.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			if name := strings.TrimSpace(p.name.Text()); name != "" {
				u.createFolder(name)
			}
			u.stopNaming()
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &p.name, Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			u.stopNaming()
		}
	}
}

// createFolder asks the daemon for the folder and selects it in the pane
// once it exists, so Enter opens it, or in move mode moves there.
func (u *ui) createFolder(name string) {
	account := u.account.Name
	u.notice = "Creating " + name + "…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		id, err := u.daemon.CreateFolder(ctx, account, name)
		u.post(func() {
			if err != nil {
				u.notice = "Couldn't create " + name + ": " + folderError(err)
				return
			}
			u.notice = "Created " + name
			if !u.pane.open || u.account.Name != account {
				return
			}
			u.refreshPaneRows()
			for i, r := range u.pane.rows {
				if !r.header && r.folder.ID == id {
					u.pane.sel = i
					u.pane.keepVisible(u)
					if len(u.pane.moving) == 0 {
						u.previewFolder(r.folder)
					}
				}
			}
		})
	}()
}

// deleteFolder deletes the selected folder after a second d. INBOX and
// the special folders can't be deleted.
func (u *ui) deleteFolder() {
	p := &u.pane
	if p.sel < 0 || p.sel >= len(p.rows) {
		return
	}
	r := p.rows[p.sel]
	f := r.folder
	switch {
	case r.header || r.other:
		return
	case u.daemon == nil:
		u.notice = "foonmaild isn't running: can't delete folders"
		return
	case f.Name == "INBOX" || f.SpecialUse != "":
		u.notice = r.label + " is one of the account's special folders and can't be deleted"
		return
	}
	if p.deleting != f.ID {
		p.deleting = f.ID
		what := "it"
		switch {
		case f.UIDValidity == 0:
			what = "whatever mail it has (it hasn't been synced)"
		case r.counts.Total == 1:
			what = "its 1 message"
		case r.counts.Total > 1:
			what = fmt.Sprintf("its %d messages", r.counts.Total)
		}
		u.notice = fmt.Sprintf("d again deletes %s and %s from the server", f.Name, what)
		return
	}
	p.deleting = 0
	u.notice = "Deleting " + f.Name + "…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := u.daemon.DeleteFolder(ctx, f.ID)
		u.post(func() {
			if err != nil {
				u.notice = "Couldn't delete " + f.Name + ": " + folderError(err)
				return
			}
			u.notice = "Deleted " + f.Name
			// Don't stay in (or go back to) a folder that is gone.
			inbox := u.inboxOf(f.AccountID)
			if u.pane.origin.ID == f.ID {
				u.pane.origin = inbox
			}
			if u.folder.ID == f.ID {
				u.showFolder(inbox)
			}
			if u.pane.open {
				sel := u.pane.sel
				u.refreshPaneRows()
				u.pane.sel = u.pane.selectable(min(sel, len(u.pane.rows)-1), -1)
				if len(u.pane.moving) == 0 && u.pane.sel < len(u.pane.rows) {
					u.previewFolder(u.pane.rows[u.pane.sel].folder)
				}
			}
		})
	}()
}

func (u *ui) inboxOf(accountID int64) cache.Folder {
	if a := u.accountByID(accountID); a != nil {
		return a.inbox
	}
	return u.pane.origin
}

// folderError words a daemon error for the status line.
func folderError(err error) string {
	switch {
	case errors.Is(err, rpc.ErrOffline):
		return "the account is offline"
	case errors.Is(err, rpc.ErrNotRunning):
		return "foonmaild isn't running"
	}
	return err.Error()
}

// nameBar is the field for a new folder's name, under the list.
func (u *ui) nameBar(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				l := material.Label(u.th, u.th.TextSize, "New folder in "+u.account.Name+": ")
				l.Color = u.muted
				return l.Layout(gtx)
			}),
			layout.Flexed(1, material.Editor(u.th, &u.pane.name, "name, with / for a subfolder (Clients/Acme)").Layout),
		)
	})
}

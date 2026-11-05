package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"foonmail/internal/cache"
	"foonmail/internal/rpc"
)

// moveTarget is where a, s and d send a conversation.
type moveTarget int

const (
	toArchive moveTarget = iota
	toJunk
	toTrash
)

var targetFlags = map[moveTarget]string{toArchive: `\Archive`, toJunk: `\Junk`, toTrash: `\Trash`}
var targetNames = map[moveTarget]string{toArchive: "Archive", toJunk: "Junk", toTrash: "Trash"}

// destination finds the folder for a move: the same Archive, Junk and
// Trash the folder pane shows.
func (u *ui) destination(t moveTarget) (cache.Folder, string, bool) {
	folders, err := u.db.Folders(context.Background(), u.accountID)
	if err != nil {
		return cache.Folder{}, "", false
	}
	slices.SortFunc(folders, func(a, b cache.Folder) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	special, yearLabel := specialFolders(folders, u.now().Year())
	f, ok := special[targetFlags[t]]
	name := f.Name
	if t == toArchive && yearLabel != "" {
		name = yearLabel
	}
	return f, name, ok
}

// moveConversation moves the selected conversation's messages in this
// folder (your replies in Sent stay put). It leaves the list at once and
// comes back if the server refuses. In the conversation view, the next
// conversation opens. d in Trash deletes for good, after a second press.
func (u *ui) moveConversation(t moveTarget) {
	c := u.current()
	if c == nil {
		return
	}
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: can't move mail"
		return
	}
	dest, name, ok := u.destination(t)
	if !ok {
		u.notice = "This account has no " + targetNames[t] + " folder"
		return
	}
	uids := c.folderUIDs(u.folder.ID, false)
	if len(uids) == 0 {
		return
	}
	key := c.key
	if dest.ID == u.folder.ID {
		if t != toTrash {
			u.notice = "Already in " + name
			return
		}
		if u.pendingDelete != key {
			u.pendingDelete = key
			u.notice = "d again deletes this conversation for good"
			return
		}
	}
	forever := dest.ID == u.folder.ID
	u.pendingDelete = ""

	// Out of the list now; the server follows.
	folder, inView := u.folder.ID, u.mode == modeView
	set := map[uint32]bool{}
	for _, uid := range uids {
		set[uid] = true
	}
	u.entries = slices.DeleteFunc(u.entries, func(e cache.Entry) bool { return e.FolderID == folder && set[e.UID] })
	at := u.sel
	u.convs = buildConversations(u.entries, folder, u.own)
	u.applyFilter("")
	u.moveTo(at) // the conversation that took its place
	if inView {
		if len(u.view) > 0 {
			u.open()
		} else {
			u.mode = modeList
			u.focusReq = u.listTag
		}
	}
	what := fmt.Sprintf("%d message", len(uids))
	if len(uids) != 1 {
		what += "s"
	}
	if forever {
		u.notice = "Deleted " + what + " for good"
	} else {
		u.notice = "Moved " + what + " to " + name
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var err error
		if forever {
			err = u.daemon.DeleteMessages(ctx, folder, uids)
		} else {
			err = u.daemon.MoveMessages(ctx, folder, uids, dest.ID)
		}
		if err == nil {
			return
		}
		u.post(func() {
			if u.folder.ID == folder {
				u.reload() // it's still there; show it again
			}
			switch {
			case errors.Is(err, rpc.ErrOffline):
				u.notice = "Offline: the messages weren't moved"
			default:
				u.notice = "Couldn't move the messages: " + err.Error()
			}
		})
	}()
}

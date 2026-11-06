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

// movingKey is a message the GUI has asked the daemon to move or delete.
type movingKey struct {
	folder int64
	uid    uint32
}

// targets is what a, s, d, v and N act on: in the list, the marked
// conversations that are shown, or else the selected one; in the
// conversation view, the open one.
func (u *ui) targets() []*conversation {
	if u.mode == modeList && len(u.marks) > 0 {
		var out []*conversation
		for _, i := range u.view {
			if c := &u.convs[i]; u.marks[c.key] {
				out = append(out, c)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if c := u.current(); c != nil {
		return []*conversation{c}
	}
	return nil
}

// moveConversation moves the targeted conversations' messages in this
// folder (your replies in Sent stay put) to Archive, Junk or Trash. d in
// Trash deletes for good, after a second press.
func (u *ui) moveConversation(t moveTarget) {
	convs := u.targets()
	if len(convs) == 0 {
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
	u.moveConvs(convs, dest, name, t == toTrash)
}

// moveConvs moves conversations' messages in this folder to dest, or
// deletes them for good when dest is this folder and deleting is allowed
// (d in Trash, asked twice). They leave the list at once and stay out of
// it while the daemon works, even as it reloads after each move of a
// burst; they come back if the server refuses. In the conversation view,
// the next conversation opens.
func (u *ui) moveConvs(convs []*conversation, dest cache.Folder, name string, deleting bool) {
	folder := u.folder.ID
	var uids []uint32
	var keys []string
	for _, c := range convs {
		uids = append(uids, c.folderUIDs(folder, false)...)
		keys = append(keys, c.key)
	}
	if len(uids) == 0 {
		return
	}
	pending := strings.Join(keys, "\x00")
	if dest.ID == folder {
		if !deleting {
			u.notice = "Already in " + name
			return
		}
		if u.pendingDelete != pending {
			u.pendingDelete = pending
			u.notice = "d again deletes " + conversations(len(convs)) + " for good"
			return
		}
	}
	forever := dest.ID == folder
	u.pendingDelete = ""

	// The conversation to select afterwards: the first one after the
	// selection that stays, or else the last one before it.
	gone := map[string]bool{}
	for _, k := range keys {
		gone[k] = true
	}
	next := ""
	for i := u.sel; i < len(u.view) && next == ""; i++ {
		if k := u.convs[u.view[i]].key; !gone[k] {
			next = k
		}
	}
	for i := min(u.sel, len(u.view)-1); i >= 0 && next == ""; i-- {
		if k := u.convs[u.view[i]].key; !gone[k] {
			next = k
		}
	}

	for _, uid := range uids {
		u.moving[movingKey{folder, uid}] = true
	}
	u.clearMarks()
	inView := u.mode == modeView
	u.entries = slices.DeleteFunc(u.entries, func(e cache.Entry) bool { return u.moving[movingKey{e.FolderID, e.UID}] })
	u.convs = buildConversations(u.entries, folder, u.own)
	u.applyFilter(next)
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
			return // the reload after the daemon's FolderChanged lets go of them
		}
		u.post(func() {
			for _, uid := range uids {
				delete(u.moving, movingKey{folder, uid})
			}
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

func conversations(n int) string {
	if n == 1 {
		return "this conversation"
	}
	return fmt.Sprintf("%d conversations", n)
}

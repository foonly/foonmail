package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"foonmail/internal/cache"
	"foonmail/internal/compose"
)

// autosaveDelay is how long typing must pause before the message being
// written is saved locally.
const autosaveDelay = 2 * time.Second

// unsent is the message being written, kept on disk in case foonmail is
// closed or crashes before it is sent or saved to Drafts.
type unsent struct {
	Draft     compose.Draft
	Answering *cache.Entry `json:",omitempty"` // the message replied to
	DraftOf   *cache.Entry `json:",omitempty"` // the stored draft being continued
	Saved     time.Time
}

// autosaveDue saves the message once typing has paused, and otherwise asks
// for a frame when it will have.
func (u *ui) autosaveDue(gtx layout.Context) {
	c := u.comp
	if c == nil || !c.dirty || c.autosave == "" {
		return
	}
	due := c.editedAt.Add(autosaveDelay)
	if u.now().Before(due) {
		gtx.Execute(op.InvalidateCmd{At: due})
		return
	}
	u.autosave()
}

// autosave writes the message being written to the autosave file, if it
// changed since the last time.
func (u *ui) autosave() {
	c := u.comp
	if c == nil || !c.dirty || c.autosave == "" {
		return
	}
	c.dirty = false
	data, err := json.Marshal(unsent{Draft: *c.draft(), Answering: c.answering, DraftOf: c.draftOf, Saved: u.now()})
	if err == nil {
		err = writeAtomic(c.autosave, data)
	}
	if err != nil {
		u.log.Warn("keeping a copy of the message being written", "err", err)
		u.notice = "Couldn't keep a copy of this message on disk: " + err.Error()
	}
}

// removeAutosave deletes the autosave file once its message has been
// sent, saved to Drafts or discarded.
func (u *ui) removeAutosave(c *composer) {
	if c.autosave == "" {
		return
	}
	if err := os.Remove(c.autosave); err != nil && !errors.Is(err, fs.ErrNotExist) {
		u.log.Warn("removing the autosaved message", "err", err)
	}
}

// restoreUnsent opens the message that was being written when foonmail
// last closed, if there is one, in the account it was written from.
func (u *ui) restoreUnsent() {
	candidates := u.accounts
	if len(candidates) == 0 { // only the account set up directly, as in tests
		candidates = []*account{{cfg: u.account, id: u.accountID, own: u.own, sent: u.sent, inbox: u.folder, autosavePath: u.autosavePath}}
	}
	for _, a := range candidates {
		if a.autosavePath == "" {
			continue
		}
		data, err := os.ReadFile(a.autosavePath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var s unsent
		if err == nil {
			err = json.Unmarshal(data, &s)
		}
		if err != nil {
			u.log.Warn("reading the autosaved message", "path", a.autosavePath, "err", err)
			u.notice = "Couldn't restore the message you were writing: see " + a.autosavePath
			continue
		}
		if a.id != u.accountID {
			u.useAccount(a)
			u.showFolder(a.inbox)
		}
		u.startCompose(&s.Draft, s.Answering)
		c := u.comp
		c.draftOf = s.DraftOf
		// Nothing of it is saved anywhere else, so Esc asks before throwing it away.
		c.initial = compose.Draft{}
		c.returnTo = modeList
		u.notice = "Restored the message you were writing (from " + s.Saved.Local().Format("Mon 15:04") + ")"
		return // one at a time; another comes back next start
	}
}

// writeAtomic replaces path with data, so a crash mid-write leaves the
// previous copy.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".unsent-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

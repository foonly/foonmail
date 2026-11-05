package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"foonmail/internal/cache"
	"foonmail/internal/compose"
	"foonmail/internal/render"
)

func (h *harness) lastSent() *render.Message {
	h.t.Helper()
	sent := h.daemon.sentMessages()
	if len(sent) == 0 {
		h.t.Fatal("nothing was sent")
	}
	m, err := render.Render(sent[len(sent)-1], render.Options{})
	if err != nil {
		h.t.Fatal(err)
	}
	return m
}

func TestNewMessage(t *testing.T) {
	h := newHarness(t)
	h.press("M")
	if h.u.mode != modeCompose || h.u.comp == nil {
		t.Fatalf("m: mode %v", h.u.mode)
	}
	if !h.r.Source().Focused(&h.u.comp.to) {
		t.Error("a new message should start in To")
	}
	if got := h.u.comp.from.Text(); got != "Kim <me@example.org>" {
		t.Errorf("From = %q", got)
	}
	h.u.comp.to.SetText("bob@example.com")
	h.u.comp.subject.SetText("Hei")
	h.u.comp.body.SetText("Mitä kuuluu?")
	h.press(key.NameReturn, key.ModCtrl)
	h.eventually("sent and back in the list", func() bool { return h.u.mode == modeList && len(h.daemon.sentMessages()) == 1 })
	m := h.lastSent()
	if m.Subject != "Hei" || m.To[0].Addr != "bob@example.com" || m.Text != "Mitä kuuluu?" {
		t.Errorf("sent: %+v", m)
	}
	if !strings.Contains(h.u.notice, "Sending to bob@example.com") {
		t.Errorf("notice = %q", h.u.notice)
	}
	// The list has the keyboard again.
	h.press("J")
	if h.selectedKey() != "x" {
		t.Errorf("j after sending: selected %q", h.selectedKey())
	}
}

func TestReplyFromList(t *testing.T) {
	h := newHarness(t)
	h.press("R") // the "Kahvi?" conversation: newest not-from-me is Liisa's
	h.eventually("compose open", func() bool { return h.u.mode == modeCompose })
	c := h.u.comp
	if c.to.Text() != "Liisa <liisa@example.com>" || !strings.HasPrefix(c.subject.Text(), "Re: ") {
		t.Errorf("To %q, Subject %q", c.to.Text(), c.subject.Text())
	}
	if !strings.Contains(c.body.Text(), "> Me too, two [1].") {
		t.Errorf("quote missing:\n%s", c.body.Text())
	}
	if !h.r.Source().Focused(&c.body) {
		t.Error("a reply should start in the body")
	}
	c.body.SetText("Count me in.\n" + c.body.Text())
	h.press(key.NameReturn, key.ModCtrl)
	h.eventually("sent", func() bool { return len(h.daemon.sentMessages()) == 1 })
	m := h.lastSent()
	if len(m.References) == 0 || m.References[len(m.References)-1] != "c@test" {
		t.Errorf("References = %v", m.References)
	}
	h.eventually("marked answered", func() bool {
		for _, f := range h.daemon.calls().flagged {
			if f == `[2] +[\Answered] -[]` {
				return true
			}
		}
		return false
	})
}

func TestReplyAllAndForwardFromView(t *testing.T) {
	h := newHarness(t)
	h.press("J") // "Invoice"
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	h.press("G")
	h.eventually("compose", func() bool { return h.u.mode == modeCompose })
	if h.u.comp.to.Text() != "Bob <bob@example.com>" {
		t.Errorf("reply-all To = %q", h.u.comp.to.Text())
	}
	// Nothing written: one Esc leaves, back to the conversation.
	h.press(key.NameEscape)
	if h.u.mode != modeView {
		t.Fatalf("Esc without changes: mode %v, want the conversation view", h.u.mode)
	}

	h.press("F")
	h.eventually("forward", func() bool { return h.u.mode == modeCompose })
	c := h.u.comp
	if c.subject.Text() != "Fwd: Invoice" || c.to.Text() != "" || !strings.Contains(c.body.Text(), "Pay it.") {
		t.Errorf("forward: subject %q, to %q, body %q", c.subject.Text(), c.to.Text(), c.body.Text())
	}
	if !h.r.Source().Focused(&c.to) {
		t.Error("a forward should start in To")
	}
}

func TestDiscardNeedsTwoEscapes(t *testing.T) {
	h := newHarness(t)
	h.press("M")
	h.u.comp.body.SetText("half-written")
	h.press(key.NameEscape)
	if h.u.mode != modeCompose || !strings.Contains(h.u.notice, "Esc again") {
		t.Fatalf("one Esc with text: mode %v, notice %q", h.u.mode, h.u.notice)
	}
	h.press(key.NameEscape)
	if h.u.mode != modeList || len(h.daemon.sentMessages()) != 0 {
		t.Errorf("second Esc: mode %v, sent %d", h.u.mode, len(h.daemon.sentMessages()))
	}
}

func TestSendProblemsStayInCompose(t *testing.T) {
	h := newHarness(t)
	h.press("M")
	h.u.comp.body.SetText("no recipient")
	h.press(key.NameReturn, key.ModCtrl)
	if h.u.mode != modeCompose || !strings.Contains(h.u.notice, "no recipients") {
		t.Errorf("mode %v, notice %q", h.u.mode, h.u.notice)
	}
	h.press("O", key.ModCtrl) // no file picker in tests
	if !strings.Contains(h.u.notice, "Attach: lines") {
		t.Errorf("notice = %q", h.u.notice)
	}
}

func TestExternalEditor(t *testing.T) {
	h := newHarness(t)
	h.u.editorArgv = func() []string {
		return []string{"sh", "-c", `sed -i 's/^Subject:.*/Subject: Edited outside/; s/^To:.*/To: pekka@example.com/' "$0"`}
	}
	h.press("M")
	h.press("E", key.ModCtrl)
	h.eventually("edited", func() bool { return h.u.comp.subject.Text() == "Edited outside" })
	if h.u.comp.to.Text() != "pekka@example.com" {
		t.Errorf("To = %q", h.u.comp.to.Text())
	}
}

// addDraft stores a draft in a new Drafts folder and switches to it.
func (h *harness) addDraft() cache.Folder {
	h.t.Helper()
	ctx := context.Background()
	drafts, _ := h.db.EnsureFolder(ctx, h.u.accountID, "Drafts")
	h.db.SetFolderInfo(ctx, drafts.ID, ".", `\Drafts`)
	h.db.SaveSyncState(ctx, drafts.ID, 1, 0, 10)
	d := compose.Reply(h.u.account, &render.Message{
		Subject: "Kahvi?", From: []render.Address{{Name: "Bob", Addr: "bob@example.com"}}, MessageID: "a@test", Text: "Kahvi?",
	}, compose.ReplySender)
	d.Body = "Half a reply\n" + d.Body
	raw, err := compose.BuildDraft(d)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.db.AddMessages(ctx, drafts.ID, []cache.NewMessage{{UID: 5, Flags: []string{`\Draft`, `\Seen`},
		Size: int64(len(raw)), Envelope: cache.Envelope{MessageID: "draft1", Subject: "Re: Kahvi?",
			From: cache.Address{Addr: "me@example.org"}, Date: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}}}); err != nil {
		h.t.Fatal(err)
	}
	id, _ := h.db.Location(ctx, drafts.ID, 5)
	if err := h.db.SetRaw(ctx, id, raw, ""); err != nil {
		h.t.Fatal(err)
	}
	drafts, _ = h.db.FolderByID(ctx, drafts.ID)
	h.u.switchFolder(drafts)
	h.frame()
	h.frame()
	return drafts
}

func TestContinueAndSendDraft(t *testing.T) {
	h := newHarness(t)
	drafts := h.addDraft()
	h.press(key.NameReturn)
	h.eventually("draft opened in compose", func() bool { return h.u.mode == modeCompose })
	c := h.u.comp
	if c.to.Text() != "Bob <bob@example.com>" || c.subject.Text() != "Re: Kahvi?" || !strings.HasPrefix(c.body.Text(), "Half a reply") {
		t.Errorf("draft fields: to %q, subject %q, body %q", c.to.Text(), c.subject.Text(), c.body.Text())
	}
	h.press(key.NameReturn, key.ModCtrl)
	h.eventually("sent", func() bool { return len(h.daemon.sentMessages()) == 1 })
	if m := h.lastSent(); m.InReplyTo != "a@test" {
		t.Errorf("the continued draft lost its threading: In-Reply-To %q", m.InReplyTo)
	}
	h.eventually("draft deleted", func() bool {
		_, deleted := h.daemon.draftCalls()
		return slices.Equal(deleted, []string{fmt.Sprintf("%d:[5]", drafts.ID)})
	})
}

func TestSaveDraft(t *testing.T) {
	h := newHarness(t)
	h.press("M")
	h.u.comp.subject.SetText("Ideas")
	h.u.comp.body.SetText("Not ready yet")
	h.press("S", key.ModCtrl)
	h.eventually("saved and closed", func() bool { return h.u.mode == modeList && h.u.notice == "Saved to Drafts" })
	saved, deleted := h.daemon.draftCalls()
	if len(saved) != 1 || len(deleted) != 0 {
		t.Fatalf("saved %d, deleted %v", len(saved), deleted)
	}
	got, err := compose.FromMessage(saved[0])
	if err != nil || got.Subject != "Ideas" || got.Body != "Not ready yet" || got.To != "" {
		t.Errorf("saved draft = %+v, %v", got, err)
	}
	if len(h.daemon.sentMessages()) != 0 {
		t.Error("saving a draft sent it")
	}
}

func TestSavingAContinuedDraftReplacesIt(t *testing.T) {
	h := newHarness(t)
	drafts := h.addDraft()
	h.press(key.NameReturn)
	h.eventually("compose", func() bool { return h.u.mode == modeCompose })
	h.u.comp.body.SetText("Changed my mind")
	h.press("S", key.ModCtrl)
	h.eventually("replaced", func() bool {
		saved, deleted := h.daemon.draftCalls()
		return len(saved) == 1 && slices.Equal(deleted, []string{fmt.Sprintf("%d:[5]", drafts.ID)})
	})
}

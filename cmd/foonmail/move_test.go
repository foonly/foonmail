package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"

	"foonmail/internal/cache"
)

func (h *harness) addTrash() cache.Folder {
	h.t.Helper()
	ctx := context.Background()
	trash, _ := h.db.EnsureFolder(ctx, h.u.accountID, "Trash")
	h.db.SetFolderInfo(ctx, trash.ID, ".", `\Trash`)
	trash, _ = h.db.FolderByID(ctx, trash.ID)
	return trash
}

func TestArchiveFromList(t *testing.T) {
	h := newHarness(t)
	h.press("A") // the "Kahvi?" conversation: INBOX UIDs 1 and 2 (the reply in Sent stays)
	want := fmt.Sprintf("%d:[1 2]->%d", h.inbox.ID, h.archive.ID)
	h.eventually("moved", func() bool { return slices.Equal(h.daemon.moveCalls(), []string{want}) })
	if len(h.u.convs) != 1 || h.selectedKey() != "x" {
		t.Errorf("after archiving: %d conversations, selected %q", len(h.u.convs), h.selectedKey())
	}
	if !strings.Contains(h.u.notice, "Moved 2 messages to Archive") {
		t.Errorf("notice = %q", h.u.notice)
	}
	h.press("S") // no Junk folder in this account
	if !strings.Contains(h.u.notice, "no Junk folder") || len(h.daemon.moveCalls()) != 1 {
		t.Errorf("junk without a folder: notice %q, moves %v", h.u.notice, h.daemon.moveCalls())
	}
}

func TestTrashFromViewOpensTheNext(t *testing.T) {
	h := newHarness(t)
	trash := h.addTrash()
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	h.press(key.NameDeleteForward)
	want := fmt.Sprintf("%d:[1 2]->%d", h.inbox.ID, trash.ID)
	h.eventually("trashed", func() bool { return slices.Equal(h.daemon.moveCalls(), []string{want}) })
	h.eventually("next conversation open", func() bool {
		return h.u.mode == modeView && h.u.conv.conv.key == "x" && h.loaded()
	})
	h.press("D") // the last one: back to an empty list
	h.eventually("back to the list", func() bool { return h.u.mode == modeList && len(h.u.convs) == 0 })
}

func TestDeleteForeverInTrashNeedsTwoPresses(t *testing.T) {
	h := newHarness(t)
	trash := h.addTrash()
	if err := h.db.AddMessages(context.Background(), trash.ID, []cache.NewMessage{{UID: 3, Envelope: cache.Envelope{
		MessageID: "old", Subject: "Old stuff"}}}); err != nil {
		t.Fatal(err)
	}
	h.u.switchFolder(trash)
	h.press("D")
	if !strings.Contains(h.u.notice, "d again") || len(h.daemon.moveCalls()) != 0 {
		t.Fatalf("first d in Trash: notice %q", h.u.notice)
	}
	if _, deleted := h.daemon.draftCalls(); len(deleted) != 0 {
		t.Fatal("deleted after one press")
	}
	h.press("D")
	h.eventually("deleted for good", func() bool {
		_, deleted := h.daemon.draftCalls()
		return slices.Equal(deleted, []string{fmt.Sprintf("%d:[3]", trash.ID)})
	})
	if len(h.daemon.moveCalls()) != 0 {
		t.Error("deleting in Trash also moved")
	}
}

func TestFailedMoveBringsTheConversationBack(t *testing.T) {
	h := newHarness(t)
	h.daemon.mu.Lock()
	h.daemon.moveErr = errors.New("server said no")
	h.daemon.mu.Unlock()
	h.press("A")
	h.eventually("back with the reason", func() bool {
		return len(h.u.convs) == 2 && strings.Contains(h.u.notice, "server said no")
	})
}

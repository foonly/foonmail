package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"
)

func TestCreateAndDeleteFolder(t *testing.T) {
	h := newHarness(t)
	h.press("C")
	h.press("N")
	if !h.u.pane.naming {
		t.Fatal("n didn't open the name field")
	}
	h.typeText("Receipts")
	h.press(key.NameReturn)
	h.eventually("created and selected", func() bool {
		r := h.u.pane.rows[h.u.pane.sel]
		return r.folder.Name == "Receipts" && h.u.folder.Name == "Receipts"
	})
	if h.u.pane.naming || !strings.Contains(h.u.notice, "Created Receipts") {
		t.Errorf("after Enter: naming %v, notice %q", h.u.pane.naming, h.u.notice)
	}
	id := h.u.pane.rows[h.u.pane.sel].folder.ID

	// d asks first; anything else calls it off.
	h.press("D")
	if !strings.Contains(h.u.notice, "d again deletes Receipts") {
		t.Fatalf("first d: %q", h.u.notice)
	}
	h.press("K")
	if h.u.pane.deleting != 0 {
		t.Error("moving didn't call off the delete")
	}
	for h.u.pane.rows[h.u.pane.sel].folder.ID != id {
		h.press("J")
	}
	h.press("D")
	h.press("D")
	h.eventually("deleted", func() bool { return len(h.daemon.folderCallList()) == 2 && h.u.folder.ID != id && h.u.folder.ID != 0 })
	want := []string{"create home Receipts", fmt.Sprint("delete ", id)}
	if got := h.daemon.folderCallList(); !slices.Equal(got, want) {
		t.Errorf("daemon calls %v, want %v", got, want)
	}
	for _, r := range h.u.pane.rows {
		if r.folder.ID == id {
			t.Error("deleted folder still in the pane")
		}
	}

	// INBOX can't go.
	for h.u.pane.rows[h.u.pane.sel].folder.Name != "INBOX" {
		h.press("K")
	}
	h.press("D")
	if !strings.Contains(h.u.notice, "can't be deleted") {
		t.Errorf("deleting INBOX: %q", h.u.notice)
	}
}

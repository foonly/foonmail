package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"foonmail/internal/cache"
	"foonmail/internal/rpc"
)

// typeFilter opens the / filter with text in it, as typing would.
func (h *harness) typeFilter(text string) {
	h.press("/", key.ModShift)
	h.u.search.SetText(text)
	h.u.applyFilter(h.selectedKey())
	h.frame()
}

func TestServerSearch(t *testing.T) {
	h := newHarness(t) // INBOX: Kahvi? (UIDs 1, 2), Invoice (3); Archive empty
	ctx := context.Background()
	h.typeFilter("invoice")
	h.press(key.NameReturn, key.ModCtrl)
	if !h.u.showingResults() {
		t.Fatal("Ctrl+Enter in the filter didn't start a server search")
	}
	h.eventually("the daemon's search id", func() bool { return h.u.server.id == 7 })
	if got := h.daemon.searchCallList(); !slices.Equal(got, []string{"search home invoice"}) {
		t.Errorf("daemon calls %q", got)
	}
	if len(h.u.view) != 0 || !strings.Contains(strings.Join(h.u.resultsHeader(), " · "), "searching") {
		t.Errorf("before results: %d rows, header %q", len(h.u.view), h.u.resultsHeader())
	}

	// The daemon stores what it finds, then says so: INBOX's Invoice by
	// its subject, then an archived message by its body.
	err := h.db.AddMessages(ctx, h.archive.ID, []cache.NewMessage{{UID: 5, Envelope: cache.Envelope{
		MessageID: "old", Subject: "Paperwork", From: cache.Address{Name: "Kim", Addr: "kim@example.org"},
		Date: time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)}}})
	if err != nil {
		t.Fatal(err)
	}
	h.u.searchProgress(rpc.SearchProgress{Search: 7, FolderID: h.inbox.ID, Folder: "INBOX", N: 1, Of: 3})
	if hd := strings.Join(h.u.resultsHeader(), " · "); !strings.Contains(hd, "searching headers in INBOX (1 of 3)") {
		t.Errorf("header while searching INBOX: %q", hd)
	}
	h.u.searchFound(rpc.SearchFound{Search: 7, FolderID: h.inbox.ID, UIDs: []uint32{3}})
	h.u.searchSkipped(rpc.SearchSkipped{Search: 7, FolderID: h.sent.ID, Folder: "Sent", Reason: "no answer in 30s"})
	h.u.searchFound(rpc.SearchFound{Search: 7, FolderID: h.archive.ID, UIDs: []uint32{5}, InBody: true})
	h.u.searchFound(rpc.SearchFound{Search: 99, FolderID: h.inbox.ID, UIDs: []uint32{1}}) // another search's
	h.u.searchDone(rpc.SearchDone{Search: 7})
	h.frame()
	if hd := strings.Join(h.u.resultsHeader(), " · "); !strings.Contains(hd, "done in") ||
		!strings.Contains(hd, "skipped Sent (headers: no answer in 30s)") {
		t.Errorf("header when done: %q", hd)
	}
	var rows []string
	for _, i := range h.u.view {
		c := &h.u.convs[i]
		rows = append(rows, h.u.server.names[c.folder]+" "+c.subject)
	}
	if !slices.Equal(rows, []string{"INBOX Invoice", "Archive Paperwork"}) {
		t.Fatalf("results %q", rows)
	}
	if s := h.u.server; s.onlyInBody(&h.u.convs[h.u.view[0]]) || !s.onlyInBody(&h.u.convs[h.u.view[1]]) {
		t.Error("only the archived one should be marked as found in its body")
	}
	h.press("D")
	if len(h.daemon.moveCalls()) != 0 || !strings.Contains(h.u.notice, "Enter") {
		t.Errorf("d in the results: moves %v, notice %q", h.daemon.moveCalls(), h.u.notice)
	}

	// Enter reads a result in its own folder; closing it comes back.
	h.press("J")
	h.press(key.NameReturn)
	if h.u.mode != modeView || h.u.folder.ID != h.archive.ID || h.u.conv.conv.key != "old" {
		t.Fatalf("Enter: mode %v, folder %q", h.u.mode, h.u.folder.Name)
	}
	h.press(key.NameEscape)
	if !h.u.showingResults() || h.u.folder.ID != h.inbox.ID || len(h.u.view) != 2 || h.selectedKey() != "old" {
		t.Fatalf("after closing: results %v, folder %q, %d rows, selected %q",
			h.u.showingResults(), h.u.folder.Name, len(h.u.view), h.selectedKey())
	}

	// Esc goes back to the list with the filter as it was.
	h.press(key.NameEscape)
	if h.u.server != nil || !h.u.searching || h.u.search.Text() != "invoice" || h.u.folder.ID != h.inbox.ID {
		t.Errorf("after Esc: server %v, searching %v %q, folder %q",
			h.u.server != nil, h.u.searching, h.u.search.Text(), h.u.folder.Name)
	}
}

func TestServerSearchCancel(t *testing.T) {
	h := newHarness(t)
	h.typeFilter("invoice")
	h.press(key.NameReturn, key.ModCtrl)
	h.eventually("the daemon's search id", func() bool { return h.u.server != nil && h.u.server.id == 7 })
	h.press(key.NameEscape)
	h.eventually("cancel", func() bool { return slices.Contains(h.daemon.searchCallList(), "cancel 7") })
	if h.u.server != nil {
		t.Error("Esc didn't leave the results")
	}
}

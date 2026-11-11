package main

import (
	"strings"
	"testing"

	"foonmail/internal/render"
)

func TestFoldSignatures(t *testing.T) {
	long := "Hello.\n\n--\n\n*Liisa Example*\n\nExample Oy\nExample Street 1\n00100 Helsinki\nliisa@example.com | www.example.org [1]\n"
	got, marks := foldSignatures(long, []textSpan{
		{start: 0, end: 5, kind: spanLink}, // before: kept
		{start: strings.Index(long, "[1]"), end: strings.Index(long, "[1]") + 3, kind: spanLink}, // inside: dropped
	})
	want := "Hello.\n\n--\n\n*Liisa Example*\n\nExample Oy\n[… 3 more lines of signature · h shows them]\n"
	if got != want {
		t.Errorf("long signature:\n%s\nwant:\n%s", got, want)
	}
	if len(marks) != 2 || marks[0].kind != spanLink || marks[1].kind != spanMuted ||
		got[marks[1].start:marks[1].end] != "[… 3 more lines of signature · h shows them]" {
		t.Errorf("marks = %+v", marks)
	}

	// Short ones stay: folding three lines into a note saves nothing.
	short := "Hello.\n\n-- \nKim Example\nExample Oy\nkim@example.org\n"
	if got, _ := foldSignatures(short, nil); got != short {
		t.Errorf("short signature changed:\n%s", got)
	}

	// A signature ends at the quote below it, which stays.
	above := "Fine.\n\n-- \nKim\nExample Oy\nStreet 1\nCity\nPhone\n\n> Can you?\n> Please.\n"
	got, _ = foldSignatures(above, nil)
	if want := "Fine.\n\n-- \nKim\nExample Oy\n[… 3 more lines of signature · h shows them]\n\n> Can you?\n> Please.\n"; got != want {
		t.Errorf("above a quote:\n%s", got)
	}
}

func TestBodyViewFoldsSignatures(t *testing.T) {
	m := &render.Message{Text: "Fixed.\n\n-- \nKim Example\nExample Oy\nStreet 1\nCity\nPhone"}
	if text, _ := bodyView(m, false); !strings.Contains(text, "[… 3 more lines of signature") || strings.Contains(text, "Phone") {
		t.Errorf("folded:\n%s", text)
	}
	if text, _ := bodyView(m, true); text != m.Text {
		t.Errorf("h shows it all:\n%s", text)
	}
}

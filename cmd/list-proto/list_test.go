package main

import (
	"testing"
	"time"
)

func testUI(n int) *ui {
	u := &ui{all: generate(n, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)), rowH: 30, viewH: 300}
	for i := range u.all {
		u.view = append(u.view, i)
	}
	return u
}

func TestEnsureVisible(t *testing.T) {
	u := testUI(1000) // 10 rows visible
	u.moveTo(9)
	if u.list.Position.First != 0 {
		t.Errorf("row 9 visible from top: First = %d, want 0", u.list.Position.First)
	}
	u.moveTo(10)
	if u.list.Position.First != 1 {
		t.Errorf("row 10: First = %d, want 1", u.list.Position.First)
	}
	u.moveTo(500)
	if f := u.list.Position.First; f != 491 {
		t.Errorf("row 500: First = %d, want 491", f)
	}
	u.moveTo(480)
	if f := u.list.Position.First; f != 480 {
		t.Errorf("moving up past the top: First = %d, want 480", f)
	}
	u.moveTo(-5)
	if u.sel != 0 {
		t.Errorf("clamp low: sel = %d", u.sel)
	}
	u.moveTo(5000)
	if u.sel != 999 {
		t.Errorf("clamp high: sel = %d", u.sel)
	}
}

func TestRowAt(t *testing.T) {
	u := testUI(100)
	u.list.Position.First, u.list.Position.Offset = 20, 10
	if got := u.rowAt(25); got != 21 {
		t.Errorf("rowAt(25) = %d, want 21", got)
	}
	if got := u.rowAt(5000); got != -1 {
		t.Errorf("rowAt past the end = %d, want -1", got)
	}
}

func TestMatchesAll(t *testing.T) {
	s := "örjan ääkkönen kahvitauko torstaina?"
	if !matchesAll(s, []string{"örjan", "kahvi"}) {
		t.Error("expected match on both words")
	}
	if matchesAll(s, []string{"örjan", "lounas"}) {
		t.Error("expected no match when one word is missing")
	}
}

func TestShortDate(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	cases := map[time.Time]string{
		time.Date(2026, 11, 4, 9, 5, 0, 0, time.UTC):  "09:05",
		time.Date(2026, 3, 1, 9, 5, 0, 0, time.UTC):   "1 Mar",
		time.Date(2025, 12, 31, 9, 5, 0, 0, time.UTC): "2025-12-31",
	}
	for in, want := range cases {
		if got := shortDate(in, now); got != want {
			t.Errorf("shortDate(%v) = %q, want %q", in, got, want)
		}
	}
}

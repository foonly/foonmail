package main

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// harness drives the UI through Gio's input router without a window.
type harness struct {
	t *testing.T
	u *ui
	r input.Router
}

func newHarness(t *testing.T) *harness {
	u := testUI(100)
	u.th = material.NewTheme()
	u.listTag = new(int)
	u.list = widget.List{List: layout.List{Axis: layout.Vertical}}
	u.bodyList = widget.List{List: layout.List{Axis: layout.Vertical}}
	u.focusReq = u.listTag
	h := &harness{t: t, u: u}
	h.frame()
	h.frame()
	return h
}

func (h *harness) frame() {
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Constraints: layout.Exact(image.Pt(800, 600)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Source:      h.r.Source(),
	}
	h.u.update(gtx)
	h.u.layout(gtx)
	h.r.Frame(&ops)
}

func (h *harness) press(name key.Name) {
	h.r.Queue(key.Event{Name: name, State: key.Press})
	h.frame()
	h.frame() // let focus commands from the previous frame settle
}

func TestEscFromViewKeepsListFocus(t *testing.T) {
	h := newHarness(t)

	h.press("J")
	if h.u.sel != 1 {
		t.Fatalf("j in the list: sel = %d, want 1", h.u.sel)
	}
	h.press(key.NameReturn)
	if h.u.mode != modeView {
		t.Fatal("Enter did not open the message")
	}
	h.press(key.NameEscape)
	if h.u.mode != modeList {
		t.Fatal("Esc did not return to the list")
	}
	h.press("J")
	if h.u.sel != 2 {
		t.Errorf("j after returning with Esc: sel = %d, want 2 (list lost keyboard focus)", h.u.sel)
	}
}

func TestEscFromFilterKeepsListFocus(t *testing.T) {
	h := newHarness(t)

	h.press("/")
	if !h.u.searching {
		t.Fatal("/ did not open the filter")
	}
	h.press(key.NameEscape)
	if h.u.searching {
		t.Fatal("Esc did not close the filter")
	}
	h.press("J")
	if h.u.sel != 1 {
		t.Errorf("j after closing the filter: sel = %d, want 1 (list lost keyboard focus)", h.u.sel)
	}
}

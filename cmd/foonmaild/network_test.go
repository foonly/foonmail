package main

import "testing"

func TestNetworkUp(t *testing.T) {
	for _, c := range []struct {
		prev, cur uint32
		want      bool
	}{
		{20, 70, true},  // disconnected → online
		{0, 70, true},   // unknown at start → online
		{60, 70, true},  // after a captive portal login
		{40, 60, false}, // connected, but the internet isn't reachable yet
		{70, 70, false}, // nothing changed
		{70, 50, false}, // going down
	} {
		if got := networkUp(c.prev, c.cur); got != c.want {
			t.Errorf("networkUp(%d, %d) = %v, want %v", c.prev, c.cur, got, c.want)
		}
	}
}

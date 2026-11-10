package main

import (
	"context"
	"log/slog"

	"github.com/godbus/dbus/v5"
)

// NetworkManager's states (NMState) that matter here.
const (
	nmUnknown         = 0
	nmConnectedGlobal = 70 // full connectivity: the internet is reachable
)

// networkUp says whether a NetworkManager state change means the internet
// just became reachable: started at boot without a network, after a
// captive portal login, or on joining a network after losing one.
func networkUp(prev, cur uint32) bool {
	return cur == nmConnectedGlobal && prev != nmConnectedGlobal
}

// watchNetwork calls wake each time NetworkManager reports that the
// internet became reachable, so the sync loops connect at once instead of
// at their next retry, which can be a minute away. Without NetworkManager
// on the system bus it does nothing, and the retries still connect.
func watchNetwork(ctx context.Context, wake func(), log *slog.Logger) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Info("no system bus: not watching the network", "err", err)
		return
	}
	defer conn.Close()
	const (
		nmName = "org.freedesktop.NetworkManager"
		nmPath = "/org/freedesktop/NetworkManager"
	)
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(nmPath),
		dbus.WithMatchInterface(nmName),
		dbus.WithMatchMember("StateChanged"),
	); err != nil {
		log.Info("can't watch NetworkManager", "err", err)
		return
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)

	var state uint32 = nmUnknown
	if v, err := conn.Object(nmName, nmPath).GetProperty(nmName + ".State"); err != nil {
		log.Info("NetworkManager isn't running: not watching the network", "err", err)
	} else if s, ok := v.Value().(uint32); ok {
		state = s
	}
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if sig.Name != nmName+".StateChanged" || len(sig.Body) != 1 {
				continue
			}
			cur, ok := sig.Body[0].(uint32)
			if !ok {
				continue
			}
			if networkUp(state, cur) {
				log.Info("network is up, reconnecting")
				wake()
			}
			state = cur
		}
	}
}

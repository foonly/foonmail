// Package tray shows a system tray icon through the StatusNotifierItem
// D-Bus protocol, which KDE, waybar, Quickshell and most Wayland bars
// implement. GNOME needs an extension.
//
// The icon is given by name, so it comes from the user's icon theme. There
// is no menu: left click calls OnActivate, and the tooltip carries details.
// The item registers again whenever the tray host (the StatusNotifierWatcher)
// restarts, which happens every time a Quickshell config is reloaded.
package tray

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	itemIface   = "org.kde.StatusNotifierItem"
	itemPath    = dbus.ObjectPath("/StatusNotifierItem")
	watcherName = "org.kde.StatusNotifierWatcher"
	watcherPath = dbus.ObjectPath("/StatusNotifierWatcher")
)

// Status is the item's status. Hosts usually highlight NeedsAttention and
// may hide Passive.
type Status string

const (
	Active         Status = "Active"
	NeedsAttention Status = "NeedsAttention"
	Passive        Status = "Passive"
)

// State is what the icon shows.
type State struct {
	Status   Status
	IconName string // from the icon theme, e.g. "mail-unread"
	Tooltip  string // first line is the title, the rest the body
}

type pixmap struct {
	W, H int32
	Data []byte
}

type tooltip struct {
	IconName string
	Pixmaps  []pixmap
	Title    string
	Text     string
}

// Tray is a registered tray item.
type Tray struct {
	conn       *dbus.Conn
	name       string
	props      *prop.Properties
	log        *slog.Logger
	onActivate func()
	signals    chan *dbus.Signal

	mu    sync.Mutex
	state State
}

// New exports a tray item on conn and registers it with the tray host, now
// and whenever the host restarts. onActivate is called on left click, from
// a D-Bus goroutine.
func New(conn *dbus.Conn, id string, initial State, onActivate func(), log *slog.Logger) (*Tray, error) {
	t := &Tray{
		conn:       conn,
		name:       fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid()),
		log:        log,
		onActivate: onActivate,
		state:      initial,
		signals:    make(chan *dbus.Signal, 8),
	}
	title, text := splitTooltip(initial.Tooltip)
	props, err := prop.Export(conn, itemPath, prop.Map{itemIface: {
		"Category":   {Value: "Communications", Emit: prop.EmitTrue},
		"Id":         {Value: id, Emit: prop.EmitTrue},
		"Title":      {Value: id, Emit: prop.EmitTrue},
		"Status":     {Value: string(initial.Status), Emit: prop.EmitTrue},
		"IconName":   {Value: initial.IconName, Emit: prop.EmitTrue},
		"IconPixmap": {Value: []pixmap{}, Emit: prop.EmitTrue},
		// Hosts show the attention icon instead of IconName while the status is
		// NeedsAttention; left empty, that state shows a placeholder.
		"AttentionIconName": {Value: initial.IconName, Emit: prop.EmitTrue},
		"ToolTip":           {Value: tooltip{Title: title, Text: text, Pixmaps: []pixmap{}}, Emit: prop.EmitTrue},
		"ItemIsMenu":        {Value: false, Emit: prop.EmitTrue},
	}})
	if err != nil {
		return nil, err
	}
	t.props = props
	if err := conn.Export(item{t}, itemPath, itemIface); err != nil {
		return nil, err
	}
	node := &introspect.Node{
		Name: string(itemPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       itemIface,
				Methods:    itemMethods,
				Signals:    itemSignals,
				Properties: props.Introspection(itemIface),
			},
		},
	}
	if err := conn.Export(introspect.NewIntrospectable(node), itemPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return nil, err
	}

	reply, err := conn.RequestName(t.name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("bus name %s is taken", t.name)
	}

	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	); err != nil {
		return nil, err
	}
	conn.Signal(t.signals)
	go t.watchHost()

	t.register()
	return t, nil
}

// Set changes what the icon shows, telling the host about each change.
func (t *Tray) Set(s State) {
	t.mu.Lock()
	old := t.state
	t.state = s
	t.mu.Unlock()

	if s.Status != old.Status {
		t.props.SetMust(itemIface, "Status", string(s.Status))
		t.emit("NewStatus", string(s.Status))
	}
	if s.IconName != old.IconName {
		t.props.SetMust(itemIface, "IconName", s.IconName)
		t.emit("NewIcon")
		t.props.SetMust(itemIface, "AttentionIconName", s.IconName)
		t.emit("NewAttentionIcon")
	}
	if s.Tooltip != old.Tooltip {
		title, text := splitTooltip(s.Tooltip)
		t.props.SetMust(itemIface, "ToolTip", tooltip{Title: title, Text: text, Pixmaps: []pixmap{}})
		t.emit("NewToolTip")
	}
}

// Close removes the item from the tray.
func (t *Tray) Close() {
	t.conn.RemoveSignal(t.signals)
	close(t.signals)
	t.conn.ReleaseName(t.name)
}

func (t *Tray) emit(signal string, args ...any) {
	if err := t.conn.Emit(itemPath, itemIface+"."+signal, args...); err != nil {
		t.log.Warn("tray: emit", "signal", signal, "err", err)
	}
}

// register announces the item to the tray host, if one is running.
func (t *Tray) register() {
	err := t.conn.Object(watcherName, watcherPath).
		Call(watcherName+".RegisterStatusNotifierItem", 0, t.name).Err
	if err != nil {
		t.log.Info("tray: no tray host yet; will register when one appears", "err", err)
		return
	}
	t.log.Debug("tray: registered with the tray host")
}

// watchHost registers again when a tray host starts or restarts.
func (t *Tray) watchHost() {
	for sig := range t.signals {
		if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) != 3 {
			continue
		}
		name, _ := sig.Body[0].(string)
		newOwner, _ := sig.Body[2].(string)
		if name == watcherName && newOwner != "" {
			t.log.Info("tray: tray host (re)started")
			t.register()
		}
	}
}

func splitTooltip(s string) (title, text string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// item holds the exported D-Bus methods, so Tray's own methods aren't exported.
type item struct{ t *Tray }

func (i item) Activate(x, y int32) *dbus.Error {
	if i.t.onActivate != nil {
		i.t.onActivate()
	}
	return nil
}

func (i item) SecondaryActivate(x, y int32) *dbus.Error { return nil }
func (i item) ContextMenu(x, y int32) *dbus.Error       { return nil }
func (i item) Scroll(delta int32, orientation string) *dbus.Error {
	return nil
}

var xy = []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}

var itemMethods = []introspect.Method{
	{Name: "Activate", Args: xy},
	{Name: "SecondaryActivate", Args: xy},
	{Name: "ContextMenu", Args: xy},
	{Name: "Scroll", Args: []introspect.Arg{
		{Name: "delta", Type: "i", Direction: "in"}, {Name: "orientation", Type: "s", Direction: "in"}}},
}

var itemSignals = []introspect.Signal{
	{Name: "NewTitle"}, {Name: "NewIcon"}, {Name: "NewAttentionIcon"}, {Name: "NewToolTip"},
	{Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s"}}},
}

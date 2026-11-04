// Package notify sends desktop notifications through the freedesktop
// org.freedesktop.Notifications D-Bus interface and delivers their action
// and close events.
package notify

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	busName = "org.freedesktop.Notifications"
	objPath = "/org/freedesktop/Notifications"
	iface   = "org.freedesktop.Notifications"
)

// Close reasons from the specification.
const (
	ClosedExpired   uint32 = 1
	ClosedDismissed uint32 = 2
	ClosedByCall    uint32 = 3
	ClosedUndefined uint32 = 4
)

// Action is a button on a notification. The key "default" is the action
// for clicking the notification itself and usually shows no button.
type Action struct {
	Key, Label string
}

// Notification is one notification to show.
type Notification struct {
	AppName    string
	ReplacesID uint32 // update this notification in place instead of adding one
	Icon       string // icon name from the theme, or a file path
	Summary    string
	Body       string // plain text; escaped if the server parses markup
	Actions    []Action
	Category   string // e.g. "email.arrived"
	Urgency    byte   // 0 low, 1 normal, 2 critical
	// Timeout: 0 is the server's default, negative never expires.
	Timeout time.Duration

	OnAction func(key string)        // called from the signal goroutine
	OnClosed func(id, reason uint32) // called from the signal goroutine
}

// Server describes the notification server.
type Server struct {
	Name, Vendor, Version, SpecVersion string
	Capabilities                       []string
}

// Has reports whether the server has a capability such as "actions" or
// "body-markup".
func (s Server) Has(capability string) bool { return slices.Contains(s.Capabilities, capability) }

// Client sends notifications.
type Client struct {
	conn   *dbus.Conn
	obj    dbus.BusObject
	server Server

	mu       sync.Mutex
	handlers map[uint32]*Notification
	signals  chan *dbus.Signal
}

// New connects to the notification server on conn.
func New(conn *dbus.Conn) (*Client, error) {
	c := &Client{
		conn:     conn,
		obj:      conn.Object(busName, objPath),
		handlers: map[uint32]*Notification{},
		signals:  make(chan *dbus.Signal, 32),
	}
	if err := c.obj.Call(iface+".GetCapabilities", 0).Store(&c.server.Capabilities); err != nil {
		return nil, fmt.Errorf("notification server: %w", err)
	}
	c.obj.Call(iface+".GetServerInformation", 0).Store(
		&c.server.Name, &c.server.Vendor, &c.server.Version, &c.server.SpecVersion)

	for _, member := range []string{"ActionInvoked", "NotificationClosed"} {
		if err := conn.AddMatchSignal(dbus.WithMatchInterface(iface), dbus.WithMatchMember(member),
			dbus.WithMatchObjectPath(objPath)); err != nil {
			return nil, err
		}
	}
	conn.Signal(c.signals)
	go c.dispatch()
	return c, nil
}

// Server returns what the notification server reported about itself.
func (c *Client) Server() Server { return c.server }

// Send shows a notification and returns its id.
func (c *Client) Send(n *Notification) (uint32, error) {
	var actions []string
	for _, a := range n.Actions {
		actions = append(actions, a.Key, a.Label)
	}
	hints := map[string]dbus.Variant{"urgency": dbus.MakeVariant(n.Urgency)}
	if n.Category != "" {
		hints["category"] = dbus.MakeVariant(n.Category)
	}
	body := n.Body
	if c.server.Has("body-markup") {
		body = escapeMarkup(body)
	}
	timeout := int32(-1) // server default
	switch {
	case n.Timeout < 0:
		timeout = 0 // never expires
	case n.Timeout > 0:
		timeout = int32(n.Timeout / time.Millisecond)
	}

	// Hold the lock across the call so a signal for the new id can't be
	// dispatched before its handler is registered.
	c.mu.Lock()
	defer c.mu.Unlock()
	var id uint32
	err := c.obj.Call(iface+".Notify", 0, n.AppName, n.ReplacesID, n.Icon, n.Summary, body,
		actions, hints, timeout).Store(&id)
	if err != nil {
		return 0, err
	}
	c.handlers[id] = n
	return id, nil
}

// Close removes a notification from the screen.
func (c *Client) Close(id uint32) error {
	return c.obj.Call(iface+".CloseNotification", 0, id).Err
}

func (c *Client) dispatch() {
	for sig := range c.signals {
		if sig.Path != objPath || len(sig.Body) < 2 {
			continue
		}
		id, ok := sig.Body[0].(uint32)
		if !ok {
			continue
		}
		c.mu.Lock()
		n := c.handlers[id]
		if sig.Name == iface+".NotificationClosed" {
			delete(c.handlers, id)
		}
		c.mu.Unlock()
		if n == nil {
			continue // another application's notification
		}
		switch sig.Name {
		case iface + ".ActionInvoked":
			if key, ok := sig.Body[1].(string); ok && n.OnAction != nil {
				n.OnAction(key)
			}
		case iface + ".NotificationClosed":
			if reason, ok := sig.Body[1].(uint32); ok && n.OnClosed != nil {
				n.OnClosed(id, reason)
			}
		}
	}
}

var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeMarkup(s string) string { return markupEscaper.Replace(s) }

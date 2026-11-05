package rpc

import (
	"context"

	"github.com/godbus/dbus/v5"
)

// The GUI's own interface. Owning GUIBusName makes the GUI single-instance,
// and a D-Bus service file lets the bus start it when it isn't running, so
// the daemon can open a message from a notification either way.
//
//	Show()                          raise the window
//	ShowMessage(folder x, uid u)    open the conversation with that message
const (
	GUIBusName   = "io.github.foonly.Foonmail.GUI"
	GUIPath      = dbus.ObjectPath("/io/github/foonly/Foonmail/GUI")
	GUIInterface = "io.github.foonly.Foonmail.GUI1"
)

// GUIClient calls the GUI. Calls start it through D-Bus activation if it
// isn't running (when its service file is installed).
type GUIClient struct {
	obj dbus.BusObject
}

// NewGUIClient returns a client using conn, normally the session bus.
func NewGUIClient(conn *dbus.Conn) *GUIClient {
	return &GUIClient{obj: conn.Object(GUIBusName, GUIPath)}
}

// Show raises the GUI's window, starting the GUI if needed.
func (g *GUIClient) Show(ctx context.Context) error {
	return mapError(g.obj.CallWithContext(ctx, GUIInterface+".Show", 0).Err)
}

// ShowMessage opens the conversation containing a message.
func (g *GUIClient) ShowMessage(ctx context.Context, folderID int64, uid uint32) error {
	return mapError(g.obj.CallWithContext(ctx, GUIInterface+".ShowMessage", 0, folderID, uid).Err)
}

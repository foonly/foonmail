package main

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"gioui.org/io/system"

	"foonmail/internal/rpc"
)

var errAlreadyRunning = errors.New("foonmail is already running")

// exportGUI claims the GUI's bus name and serves Show and ShowMessage. If
// another foonmail owns the name, it returns errAlreadyRunning.
func exportGUI(conn *dbus.Conn, u *ui) error {
	if err := conn.Export(guiMethods{u}, rpc.GUIPath, rpc.GUIInterface); err != nil {
		return err
	}
	node := &introspect.Node{
		Name: string(rpc.GUIPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			{Name: rpc.GUIInterface, Methods: introspect.Methods(guiMethods{})},
		},
	}
	if err := conn.Export(introspect.NewIntrospectable(node), rpc.GUIPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	reply, err := conn.RequestName(rpc.GUIBusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Export(nil, rpc.GUIPath, rpc.GUIInterface)
		return errAlreadyRunning
	}
	return nil
}

// guiMethods are the exported D-Bus methods. They run on D-Bus goroutines
// and hand the work to the UI goroutine.
type guiMethods struct{ u *ui }

func (m guiMethods) Show() *dbus.Error {
	m.u.post(m.u.raise)
	return nil
}

func (m guiMethods) ShowMessage(folderID int64, uid uint32) *dbus.Error {
	m.u.post(func() {
		m.u.showMessage(folderID, uid)
		m.u.raise()
	})
	return nil
}

// raise brings the window forward and counts as the user looking. Wayland
// lets a window ask but not take focus, and Gio's raise does nothing there,
// so on Hyprland the compositor is asked directly.
func (u *ui) raise() {
	if u.w != nil {
		u.w.Perform(system.ActionRaise)
	}
	if u.focusWindow != nil {
		u.focusWindow()
	}
	u.acknowledge()
}

// hyprlandFocus focuses foonmail's window through hyprctl, if running
// under Hyprland.
func hyprlandFocus() {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return
	}
	cmd := exec.Command("hyprctl", "dispatch", "focuswindow", "class:^(foonmail)$")
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

// showMessage opens the conversation containing a message, switching
// folder if needed. A message being written is left alone.
func (u *ui) showMessage(folderID int64, uid uint32) {
	if u.mode == modeCompose {
		u.notice = "New mail arrived; send or save this message (Ctrl+S) to read it"
		return
	}
	if u.pane.open {
		u.closePane()
	}
	if u.searching {
		u.search.SetText("")
		u.searching = false
	}
	if folderID != u.folder.ID {
		f, err := u.db.FolderByID(context.Background(), folderID)
		if err != nil {
			u.notice = "Couldn't find that folder: " + err.Error()
			return
		}
		u.showFolder(f)
	} else {
		u.reload() // the message may be newer than the list
	}
	for vi, i := range u.view {
		for _, m := range u.convs[i].members {
			if m.FolderID == folderID && m.UID == uid {
				u.sel = vi
				u.ensureVisible()
				u.open()
				return
			}
		}
	}
	u.mode = modeList
	u.focusReq = u.listTag
	u.notice = "That message isn't in the cache yet"
}

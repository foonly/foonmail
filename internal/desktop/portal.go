// Package desktop holds small Linux desktop integrations shared by the prototypes.
package desktop

import (
	"context"
	"time"

	"github.com/godbus/dbus/v5"
)

// PrefersDark asks the desktop portal for the color-scheme preference
// (org.freedesktop.appearance color-scheme: 1 means dark).
func PrefersDark() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	var v dbus.Variant
	err = obj.CallWithContext(ctx, "org.freedesktop.portal.Settings.ReadOne", 0,
		"org.freedesktop.appearance", "color-scheme").Store(&v)
	if err != nil {
		// Older portals only have Read, which wraps the value in a second variant.
		err = obj.CallWithContext(ctx, "org.freedesktop.portal.Settings.Read", 0,
			"org.freedesktop.appearance", "color-scheme").Store(&v)
		if err != nil {
			return false
		}
		if inner, ok := v.Value().(dbus.Variant); ok {
			v = inner
		}
	}
	n, ok := v.Value().(uint32)
	return ok && n == 1
}

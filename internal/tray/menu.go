package tray

import (
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// The right-click menu, through com.canonical.dbusmenu, which KDE, waybar
// and Quickshell read from the item's Menu property. It is a fixed, flat
// list of items, so only the parts of the protocol that reads one are
// more than stubs.

const (
	menuIface = "com.canonical.dbusmenu"
	menuPath  = dbus.ObjectPath("/MenuBar")
)

// MenuItem is an entry in the right-click menu. Do is called from a D-Bus
// goroutine when the user picks it.
type MenuItem struct {
	Label string
	Do    func()
}

// layout is a dbusmenu item: (ia{sv}av).
type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

// itemProps is one item's properties in GetGroupProperties: (ia{sv}).
type itemProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

// menuEvent is an entry of EventGroup: (isvu).
type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

// menu holds the exported methods. Item ids are 1-based indexes into
// items; 0 is the root.
type menu struct{ items []MenuItem }

func (m menu) props(id int32) map[string]dbus.Variant {
	if id == 0 {
		return map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")}
	}
	return map[string]dbus.Variant{"label": dbus.MakeVariant(m.items[id-1].Label)}
}

func (m menu) valid(id int32) bool { return id >= 0 && int(id) <= len(m.items) }

func (m menu) GetLayout(parentID, depth int32, names []string) (uint32, layout, *dbus.Error) {
	if !m.valid(parentID) {
		return 0, layout{}, dbus.MakeFailedError(errUnknownItem)
	}
	l := layout{ID: parentID, Props: m.props(parentID), Children: []dbus.Variant{}}
	if parentID == 0 && depth != 0 {
		for i := range m.items {
			id := int32(i + 1)
			l.Children = append(l.Children,
				dbus.MakeVariant(layout{ID: id, Props: m.props(id), Children: []dbus.Variant{}}))
		}
	}
	return 1, l, nil
}

func (m menu) GetGroupProperties(ids []int32, names []string) ([]itemProps, *dbus.Error) {
	if len(ids) == 0 { // all of them
		for i := 0; i <= len(m.items); i++ {
			ids = append(ids, int32(i))
		}
	}
	out := []itemProps{}
	for _, id := range ids {
		if m.valid(id) {
			out = append(out, itemProps{ID: id, Props: m.props(id)})
		}
	}
	return out, nil
}

func (m menu) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	if m.valid(id) {
		if v, ok := m.props(id)[name]; ok {
			return v, nil
		}
	}
	return dbus.MakeVariant(""), dbus.MakeFailedError(errUnknownItem)
}

func (m menu) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID == "clicked" && id > 0 && m.valid(id) && m.items[id-1].Do != nil {
		go m.items[id-1].Do()
	}
	return nil
}

func (m menu) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	bad := []int32{}
	for _, e := range events {
		if !m.valid(e.ID) {
			bad = append(bad, e.ID)
			continue
		}
		_ = m.Event(e.ID, e.EventID, e.Data, e.Timestamp)
	}
	return bad, nil
}

func (m menu) AboutToShow(id int32) (bool, *dbus.Error) { return false, nil }

func (m menu) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

type menuError string

func (e menuError) Error() string { return string(e) }

const errUnknownItem = menuError("no such menu item")

// exportMenu puts the menu on conn at menuPath.
func exportMenu(conn *dbus.Conn, items []MenuItem) error {
	m := menu{items: items}
	if err := conn.Export(m, menuPath, menuIface); err != nil {
		return err
	}
	props, err := prop.Export(conn, menuPath, prop.Map{menuIface: {
		"Version":       {Value: uint32(3)},
		"TextDirection": {Value: "ltr"},
		"Status":        {Value: "normal"},
		"IconThemePath": {Value: []string{}},
	}})
	if err != nil {
		return err
	}
	in := func(name, typ string) introspect.Arg { return introspect.Arg{Name: name, Type: typ, Direction: "in"} }
	out := func(name, typ string) introspect.Arg { return introspect.Arg{Name: name, Type: typ, Direction: "out"} }
	node := &introspect.Node{
		Name: string(menuPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name: menuIface,
				Methods: []introspect.Method{
					{Name: "GetLayout", Args: []introspect.Arg{in("parentId", "i"), in("recursionDepth", "i"),
						in("propertyNames", "as"), out("revision", "u"), out("layout", "(ia{sv}av)")}},
					{Name: "GetGroupProperties", Args: []introspect.Arg{in("ids", "ai"), in("propertyNames", "as"),
						out("properties", "a(ia{sv})")}},
					{Name: "GetProperty", Args: []introspect.Arg{in("id", "i"), in("name", "s"), out("value", "v")}},
					{Name: "Event", Args: []introspect.Arg{in("id", "i"), in("eventId", "s"), in("data", "v"),
						in("timestamp", "u")}},
					{Name: "EventGroup", Args: []introspect.Arg{in("events", "a(isvu)"), out("idErrors", "ai")}},
					{Name: "AboutToShow", Args: []introspect.Arg{in("id", "i"), out("needUpdate", "b")}},
					{Name: "AboutToShowGroup", Args: []introspect.Arg{in("ids", "ai"),
						out("updatesNeeded", "ai"), out("idErrors", "ai")}},
				},
				Signals: []introspect.Signal{
					{Name: "ItemsPropertiesUpdated", Args: []introspect.Arg{
						{Name: "updatedProps", Type: "a(ia{sv})"}, {Name: "removedProps", Type: "a(ias)"}}},
					{Name: "LayoutUpdated", Args: []introspect.Arg{{Name: "revision", Type: "u"}, {Name: "parent", Type: "i"}}},
					{Name: "ItemActivationRequested", Args: []introspect.Arg{{Name: "id", Type: "i"}, {Name: "timestamp", Type: "u"}}},
				},
				Properties: props.Introspection(menuIface),
			},
		},
	}
	return conn.Export(introspect.NewIntrospectable(node), menuPath, "org.freedesktop.DBus.Introspectable")
}

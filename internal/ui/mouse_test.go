package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// mouseAt sends a mouse event at (x, y) after drawing the screen, as the
// program does before any event can arrive.
func mouseAt(a *App, msg func(tea.Mouse) tea.Msg, x, y int, b tea.MouseButton) tea.Cmd {
	a.render()
	_, cmd := a.Update(msg(tea.Mouse{X: x, Y: y, Button: b}))
	return cmd
}

func click(a *App, x, y int) tea.Cmd {
	return mouseAt(a, func(m tea.Mouse) tea.Msg { return tea.MouseClickMsg(m) }, x, y, tea.MouseLeft)
}

func rightClick(a *App, x, y int) tea.Cmd {
	return mouseAt(a, func(m tea.Mouse) tea.Msg { return tea.MouseClickMsg(m) }, x, y, tea.MouseRight)
}

func wheel(a *App, x, y int, down bool) tea.Cmd {
	b := tea.MouseWheelUp
	if down {
		b = tea.MouseWheelDown
	}
	return mouseAt(a, func(m tea.Mouse) tea.Msg { return tea.MouseWheelMsg(m) }, x, y, b)
}

// find is where text first appears on the screen. The header shows the
// temp folder's path, which holds the test's name, so a test's name
// mustn't contain the text it looks for.
func find(t *testing.T, a *App, text string) (x, y int) {
	t.Helper()
	for y, l := range strings.Split(screen(a), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q isn't on the screen:\n%s", text, screen(a))
	return 0, 0
}

// clickText clicks the first place text appears, and forgets the click so
// the next one isn't a double click.
func clickText(t *testing.T, a *App, text string) tea.Cmd {
	t.Helper()
	x, y := find(t, a, text)
	cmd := click(a, x, y)
	a.mouse.lastAt = time.Time{}
	return cmd
}

func TestMouseTabs(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	for i, name := range []string{"Keys", "Settings", "Discover", "Hosts"} {
		clickText(t, a, name)
		if want := []int{tabKeys, tabSettings, tabDiscover, tabHosts}[i]; a.tab != want {
			t.Fatalf("clicking %s: tab %d", name, a.tab)
		}
	}
}

// The row tests run at 90 columns, where there's no side pane repeating
// the host's name.

func TestMouseHostRows(t *testing.T) {
	e := newTestApp(t, 90, 30)
	a := e.a
	clickText(t, a, "oak2")
	if cursorAlias(a) != "oak2" || a.Outcome().Argv != nil {
		t.Fatalf("a click moves the cursor and nothing else: %s %+v", cursorAlias(a), a.Outcome())
	}

	// The mark column selects without moving the cursor.
	_, y := find(t, a, "elm ")
	click(a, 1, y)
	a.mouse.lastAt = time.Time{}
	if !a.hosts.selected["elm"] || cursorAlias(a) != "elm" {
		t.Fatalf("selected %v, cursor %s", a.hosts.selected, cursorAlias(a))
	}
	click(a, 1, y)
	a.mouse.lastAt = time.Time{}
	if a.hosts.selected["elm"] {
		t.Fatal("a second click unselects")
	}

	// Wheel moves the cursor.
	wheel(a, 10, y, true)
	if cursorAlias(a) == "elm" {
		t.Fatal("the wheel moves down")
	}
	wheel(a, 10, y, false)
	if cursorAlias(a) != "elm" {
		t.Fatalf("and back up: %s", cursorAlias(a))
	}

	// Right click is the action menu for that row.
	x, y := find(t, a, "devbox1")
	rightClick(a, x, y)
	if a.popup == nil || a.popup.title != "devbox1" {
		t.Fatalf("right click: %+v", a.popup)
	}
	press(a, "esc")

	// A double click connects.
	click(a, x, y)
	click(a, x, y)
	if o := a.Outcome(); strings.Join(o.Argv, " ") != "ssh -F "+e.sshPath+" devbox1" {
		t.Fatalf("double click: %+v", o)
	}
}

func TestMouseSlowClicksArentDouble(t *testing.T) {
	a := newTestApp(t, 90, 30).a
	x, y := find(t, a, "devbox1")
	click(a, x, y)
	a.mouse.lastAt = time.Now().Add(-time.Second)
	click(a, x, y)
	if a.Outcome().Argv != nil {
		t.Fatal("two slow clicks don't connect")
	}
}

func TestMouseClickBlursFilter(t *testing.T) {
	a := newTestApp(t, 90, 30).a
	press(a, "/", "l")
	clickText(t, a, "elm")
	if a.hosts.filter.Focused() || cursorAlias(a) != "elm" {
		t.Fatalf("focused %v, cursor %s", a.hosts.filter.Focused(), cursorAlias(a))
	}
}

func TestMouseOverlaysAreModal(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "m")
	clickText(t, a, "Keys") // the tab, above the popup
	if a.popup != nil || a.tab != tabHosts {
		t.Fatalf("a click outside closes the popup and reaches nothing else: popup %v, tab %d", a.popup != nil, a.tab)
	}

	press(a, "?")
	click(a, 0, 0)
	if a.help {
		t.Fatal("a click outside closes help")
	}

	// Forms stay open: esc would throw away what was typed.
	press(a, "n", "box")
	click(a, 0, a.h-1)
	if a.form == nil {
		t.Fatal("a click outside keeps the form")
	}
}

func TestMouseDetailWheel(t *testing.T) {
	a := newTestApp(t, 120, 12).a // the side pane is open at this width
	x := a.w - a.paneWidth() + 2
	wheel(a, x, 5, true)
	if a.detail.scroll == 0 || cursorAlias(a) != "devbox1" {
		t.Fatalf("the wheel over the pane scrolls it, not the list: scroll %d, cursor %s", a.detail.scroll, cursorAlias(a))
	}
}

func TestMouseOff(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	if a.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("on by default")
	}
	a.cfg.Mouse = false
	if a.View().MouseMode != tea.MouseModeNone {
		t.Fatal("the setting turns it off")
	}
	clickText(t, a, "Keys")
	if a.tab != tabHosts {
		t.Fatal("clicks are ignored when it's off")
	}
}

func TestMouseDiscoverRows(t *testing.T) {
	a := newTestApp(t, 130, 30).a
	withDevices(t, a)
	press(a, "2")
	clickText(t, a, "zebra")
	if d := a.disc.current(); d == nil || d.Name != "zebra" {
		t.Fatalf("cursor: %+v", d)
	}
	_, y := find(t, a, "box.lan")
	click(a, 1, y)
	a.mouse.lastAt = time.Time{}
	if len(a.selectedDevices()) != 1 || a.disc.current().Name != "box.lan" {
		t.Fatalf("the mark column selects: %v", a.selectedDevices())
	}
	press(a, "space") // unselect, so a double click adds just one
	x, y := find(t, a, "nas")
	click(a, x, y)
	click(a, x, y)
	if a.form == nil || a.form.inputs[fAlias].Value() != "nas" {
		t.Fatalf("a double click opens the add form: %+v", a.form)
	}
}

func TestMouseKeysRows(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "3")
	x, y := find(t, a, "work")
	click(a, x, y)
	if k := a.keys.current(); k == nil || k.Name != "work" {
		t.Fatalf("cursor: %+v", k)
	}
	click(a, x, y)
	if a.popup == nil || a.popup.title != "work" {
		t.Fatalf("a double click opens the key's menu: %+v", a.popup)
	}
}

func TestClickSettings(t *testing.T) {
	a := newTestApp(t, 120, 40).a
	press(a, "4")
	clickText(t, a, "Mouse")
	if it := a.settingItems()[a.settings.cursor]; it.label != "Mouse" || !a.cfg.Mouse {
		t.Fatalf("a click on the label only moves there: %q, mouse %v", it.label, a.cfg.Mouse)
	}
	_, y := find(t, a, "Mouse")
	click(a, 5, y) // the checkbox
	if a.cfg.Mouse {
		t.Fatal("a click on the checkbox ticks it")
	}
}

func TestMouseSortByColumn(t *testing.T) {
	a := newTestApp(t, 90, 30).a
	for _, c := range []struct {
		head string
		want sortMode
	}{{"HOST", sortName}, {"STATUS", sortStatus}, {"CONNECTED", sortFrecency}} {
		clickText(t, a, c.head)
		if a.hosts.sort != c.want {
			t.Fatalf("%s: sort %s", c.head, a.hosts.sort)
		}
	}
	if cursorAlias(a) == "" || a.hosts.view[0].host.Alias != "devbox1" {
		t.Fatal("the list is still there")
	}
}

func TestMousePopupAndForm(t *testing.T) {
	a := newTestApp(t, 120, 40).a
	press(a, "m")
	clickText(t, a, "Edit…")
	if a.popup != nil || a.form == nil || a.form.host == nil {
		t.Fatalf("clicking an action runs it: popup %v form %+v", a.popup != nil, a.form)
	}
	clickText(t, a, "ProxyJump")
	if a.form.focus != fJump {
		t.Fatalf("clicking a field focuses it: %d", a.form.focus)
	}
	clickText(t, a, "Delete host")
	if !a.form.confirmDelete {
		t.Fatal("Delete host asks first")
	}
	press(a, "n", "esc", "esc")

	press(a, "n")
	clickText(t, a, "Save")
	if a.form == nil || !strings.Contains(a.form.err, "alias") {
		t.Fatalf("Save checks the fields: %+v", a.form)
	}
}

func TestMouseKeyAndTunnelForms(t *testing.T) {
	a := newTestApp(t, 120, 40).a
	press(a, "3", "n")
	clickText(t, a, "rsa")
	if keyTypes[a.keyForm.typ] != "rsa" || !strings.Contains(a.keyForm.name.Value(), "rsa") {
		t.Fatalf("clicking a type picks it: %s %q", keyTypes[a.keyForm.typ], a.keyForm.name.Value())
	}
	clickText(t, a, "Comment")
	if a.keyForm.focus != 2 {
		t.Fatalf("focus %d", a.keyForm.focus)
	}
	press(a, "esc", "1")

	a.hosts.focus("elm")
	press(a, "T", "n")
	clickText(t, a, "Name")
	if a.tunnelForm == nil || a.tunnelForm.focus != 1 {
		t.Fatalf("tunnel form: %+v", a.tunnelForm)
	}
}

func TestMouseBarHelpAndDoctor(t *testing.T) {
	a := newTestApp(t, 120, 20).a
	clickText(t, a, "to fix")
	if a.doctor == nil {
		t.Fatalf("!N to fix opens the doctor:\n%s", screen(a))
	}
	x, y := find(t, a, a.findings[1].Title)
	click(a, x, y)
	if a.doctor.cursor != 1 {
		t.Fatalf("clicking a finding moves to it: %d", a.doctor.cursor)
	}
	press(a, "esc")

	clickText(t, a, "? help")
	if !a.help {
		t.Fatal("? help opens help")
	}
	_, y = find(t, a, "Everywhere")
	wheel(a, a.w/2, y, true)
	if a.helpScroll != 1 || !a.help {
		t.Fatalf("the wheel scrolls help: %d", a.helpScroll)
	}
}

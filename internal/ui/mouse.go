package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Mouse support. While render draws the screen, each part records the
// areas it makes clickable as zones, in screen cells. A mouse event goes
// to the topmost zone under it that handles that kind of event. Zone
// actions call the same code as the keys, so the mouse adds no behaviour
// of its own.

type zone struct {
	x, y, w, h int
	click      func() tea.Cmd
	dbl        func() tea.Cmd // a second click on the same cell; nil runs click again
	right      func() tea.Cmd
	wheel      func(delta int) tea.Cmd // delta is -1 (up) or 1 (down)
}

func (z zone) has(x, y int) bool {
	return x >= z.x && x < z.x+z.w && y >= z.y && y < z.y+z.h
}

// doubleClick is how soon a second click on the same cell counts as a
// double click.
const doubleClick = 400 * time.Millisecond

type mouseState struct {
	zones []zone  // from the last render, topmost last
	rec   *[]zone // where render is recording zones; nil outside render
	// The last click, for spotting a double click.
	lastAt time.Time
	lastX  int
	lastY  int
}

// addZone records z, in the coordinates of whatever is being drawn. It
// does nothing outside render (scrollDetail renders the pane to measure it).
func (a *App) addZone(z zone) {
	if a.mouse.rec != nil {
		*a.mouse.rec = append(*a.mouse.rec, z)
	}
}

// capture runs draw, collecting the zones it records relative to its
// output's top left, for the caller to place with addZones.
func (a *App) capture(draw func() string) (string, []zone) {
	var zs []zone
	prev := a.mouse.rec
	a.mouse.rec = &zs
	s := draw()
	a.mouse.rec = prev
	return s, zs
}

// addZones records zones drawn at (dx, dy).
func (a *App) addZones(zs []zone, dx, dy int) {
	for _, z := range zs {
		z.x += dx
		z.y += dy
		a.addZone(z)
	}
}

// clipZones keeps the parts of zones that are on the screen.
func clipZones(zs []zone, w, h int) []zone {
	var out []zone
	for _, z := range zs {
		x0, y0 := max(0, z.x), max(0, z.y)
		x1, y1 := min(w, z.x+z.w), min(h, z.y+z.h)
		if x1 > x0 && y1 > y0 {
			z.x, z.y, z.w, z.h = x0, y0, x1-x0, y1-y0
			out = append(out, z)
		}
	}
	return out
}

// zoneAt is the topmost zone under (x, y) that ok accepts.
func (a *App) zoneAt(x, y int, ok func(zone) bool) *zone {
	for i := len(a.mouse.zones) - 1; i >= 0; i-- {
		if z := &a.mouse.zones[i]; z.has(x, y) && ok(*z) {
			return z
		}
	}
	return nil
}

func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if !a.cfg.Mouse {
		return nil
	}
	m := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		switch m.Button {
		case tea.MouseLeft:
			double := time.Since(a.mouse.lastAt) < doubleClick && m.X == a.mouse.lastX && m.Y == a.mouse.lastY
			if double {
				a.mouse.lastAt = time.Time{} // a third click starts again
			} else {
				a.mouse.lastAt, a.mouse.lastX, a.mouse.lastY = time.Now(), m.X, m.Y
			}
			z := a.zoneAt(m.X, m.Y, func(z zone) bool { return z.click != nil || double && z.dbl != nil })
			switch {
			case z == nil:
			case double && z.dbl != nil:
				return z.dbl()
			case z.click != nil:
				return z.click()
			}
		case tea.MouseRight:
			if z := a.zoneAt(m.X, m.Y, func(z zone) bool { return z.right != nil }); z != nil {
				return z.right()
			}
		}
	case tea.MouseWheelMsg:
		delta := 0
		switch m.Button {
		case tea.MouseWheelUp:
			delta = -1
		case tea.MouseWheelDown:
			delta = 1
		}
		if delta == 0 {
			return nil
		}
		if z := a.zoneAt(m.X, m.Y, func(z zone) bool { return z.wheel != nil }); z != nil {
			return z.wheel(delta)
		}
	}
	return nil
}

// overlayZones are the zones behind an open overlay at (x, y), w×h: it's
// modal, so a click elsewhere closes it (as esc does) when that's safe,
// and nothing underneath can be reached.
func (a *App) overlayZones(x, y, w, h int) []zone {
	none := func() tea.Cmd { return nil }
	noWheel := func(int) tea.Cmd { return nil }
	outside := none
	// Forms aren't closed by a stray click: esc throws away what was typed.
	if a.form == nil && a.keyForm == nil && a.tunnelForm == nil && (a.runv == nil || a.runv.command != "") {
		outside = func() tea.Cmd { return a.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape}) }
	}
	return []zone{
		{x: 0, y: 0, w: a.w, h: a.h, click: outside, right: outside, wheel: noWheel},
		{x: x, y: y, w: w, h: h, click: none, right: none, wheel: noWheel},
	}
}

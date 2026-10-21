package ui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/sshconfig"
)

// Powerline glyphs (need a Nerd Font / Powerline font).
const (
	plRight     = "" // solid arrow pointing right
	plRightThin = ""
	plLeft      = "" // solid arrow pointing left
	plLeftThin  = ""
	plBranch    = ""
)

// part is a run of text inside a segment. Every part is rendered with the
// segment's background, since a nested style's reset would otherwise clear
// it mid-segment.
type part struct {
	text string
	fg   color.Color
	bold bool
}

type segment struct {
	parts []part
	bg    color.Color
}

func seg(bg color.Color, parts ...part) segment { return segment{parts: parts, bg: bg} }

func (s segment) empty() bool {
	for _, p := range s.parts {
		if p.text != "" {
			return false
		}
	}
	return true
}

func (s segment) width() int {
	w := 2 // padding
	for _, p := range s.parts {
		w += ansi.StringWidth(p.text)
	}
	return w
}

func (s segment) render() string {
	pad := lipgloss.NewStyle().Background(s.bg).Render(" ")
	var b strings.Builder
	b.WriteString(pad)
	for _, p := range s.parts {
		b.WriteString(lipgloss.NewStyle().Background(s.bg).Foreground(p.fg).Bold(p.bold).Render(p.text))
	}
	b.WriteString(pad)
	return b.String()
}

func sameColor(a, b color.Color) bool {
	r1, g1, b1, a1 := a.RGBA()
	r2, g2, b2, a2 := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}

// renderStatusBar draws the airline-style bar at the bottom of the screen:
// mode, the selected host and where it is defined on the left; activity,
// a summary, position and a help hint on the right.
func (a *App) renderStatusBar() string {
	st := a.st
	mode, modeBg := a.barMode()
	left := []segment{seg(modeBg, part{text: mode, fg: st.accentFg, bold: true})}
	left = append(left, a.barContext()...)
	path := a.barPath()

	activity, summary, position := a.barActivity(), a.barSummary(), a.barPosition()
	help := seg(modeBg, part{text: "? help", fg: st.accentFg, bold: true})
	right := func() []segment {
		var r []segment
		for _, s := range []segment{activity, summary, position, help} {
			if !s.empty() {
				r = append(r, s)
			}
		}
		return r
	}

	// Fit, giving things up in order of importance: shorten the path from
	// the left (the line number is at the end) down to a minimum, drop the
	// summary, drop the position, shorten the message, and finally the path.
	const minPath = 12
	room := func() int { return a.w - a.barWidth(left, right()) - 3 } // path padding and trailing space
	if room() < minPath && !summary.empty() {
		summary = segment{}
	}
	if room() < minPath && !position.empty() {
		position = segment{}
	}
	if over := minPath - room(); over > 0 && !activity.empty() {
		activity = activity.shorten(over)
	}
	if room() < 0 && len(left) > 1 {
		left = left[:1] // very narrow: keep only the mode on the left
	}
	switch r := room(); {
	case r >= ansi.StringWidth(path):
	case r >= 2:
		path = "…" + ansi.TruncateLeft(path, ansi.StringWidth(path)-r+1, "")
	default:
		path = ""
	}
	left = append(left, seg(st.barLow, part{text: path, fg: st.text}))
	return a.joinBar(left, right())
}

// shorten trims n cells off the end of the segment's text (last part
// first), keeping at least a few characters and marking the cut with "…".
func (s segment) shorten(n int) segment {
	parts := append([]part{}, s.parts...)
	for i := len(parts) - 1; i >= 0 && n > 0; i-- {
		w := ansi.StringWidth(parts[i].text)
		newW := max(5, w-n) // ansi.Truncate counts the "…" in newW
		if newW >= w {
			continue
		}
		parts[i].text = ansi.Truncate(parts[i].text, newW, "…")
		n -= w - newW
	}
	s.parts = parts
	return s
}

// barWidth is the width of all segments and separators, excluding the
// path segment's text.
func (a *App) barWidth(left, right []segment) int {
	w := len(left) + len(right) // one separator after each left and before each right segment
	for _, s := range append(append([]segment{}, left...), right...) {
		w += s.width()
	}
	return w
}

func (a *App) joinBar(left, right []segment) string {
	st := a.st
	var b strings.Builder
	sep := func(glyph string, fg, bg color.Color) {
		if a.cfg.Powerline {
			b.WriteString(lipgloss.NewStyle().Foreground(fg).Background(bg).Render(glyph))
		} else {
			b.WriteString(lipgloss.NewStyle().Background(bg).Render(" "))
		}
	}

	for i, s := range left {
		b.WriteString(s.render())
		next := st.barLow
		if i+1 < len(left) {
			next = left[i+1].bg
		}
		switch {
		case i == len(left)-1:
			// The path segment runs into the fill; no separator needed.
			b.WriteString(lipgloss.NewStyle().Background(s.bg).Render(" "))
		case sameColor(s.bg, next):
			sep(plRightThin, st.muted, s.bg)
		default:
			sep(plRight, s.bg, next)
		}
	}

	var rb strings.Builder
	prev := st.barLow
	for _, s := range right {
		if a.cfg.Powerline {
			glyph, fg := plLeft, s.bg
			if sameColor(prev, s.bg) {
				glyph, fg = plLeftThin, st.muted
			}
			rb.WriteString(lipgloss.NewStyle().Foreground(fg).Background(prev).Render(glyph))
		} else {
			rb.WriteString(lipgloss.NewStyle().Background(prev).Render(" "))
		}
		rb.WriteString(s.render())
		prev = s.bg
	}

	fill := a.w - lipgloss.Width(b.String()) - lipgloss.Width(rb.String())
	if fill > 0 {
		b.WriteString(lipgloss.NewStyle().Background(st.barLow).Render(strings.Repeat(" ", fill)))
	}
	b.WriteString(rb.String())
	return ansi.Truncate(b.String(), a.w, "")
}

// barMode names what the keyboard is currently driving, like vim's modes.
func (a *App) barMode() (string, color.Color) {
	st := a.st
	switch {
	case a.help:
		return "HELP", st.accent
	case a.form != nil && a.form.host == nil:
		return "NEW", st.green
	case a.form != nil:
		return "EDIT", st.yellow
	case a.keyForm != nil:
		return "NEW KEY", st.green
	case a.copyKey != nil && a.tab == tabHosts:
		return "PICK HOSTS", st.yellow
	case a.popup != nil:
		return "ACTIONS", st.green
	}
	if f := a.activeFilter(); f != nil && f.Focused() {
		return "FILTER", st.yellow
	}
	switch a.tab {
	case tabDiscover:
		return "DISCOVER", st.blue
	case tabKeys:
		return "KEYS", st.green
	case tabSettings:
		return "SETTINGS", st.magenta
	}
	return "HOSTS", st.accent
}

// barContext is the segment after the mode: where the selected host
// connects to.
func (a *App) barContext() []segment {
	st := a.st
	h := a.hosts.currentHost()
	if a.tab != tabHosts || h == nil {
		return nil
	}
	res := a.res(h.Alias)
	if res == nil || res.Err != nil {
		return nil
	}
	parts := []part{{text: res.Target(), fg: st.text, bold: true}}
	if j := res.Jump(); j != "" {
		parts = append(parts, part{text: " via " + j, fg: st.muted})
	}
	return []segment{seg(st.barMid, parts...)}
}

// barPath is where the selected row is defined.
func (a *App) barPath() string {
	switch a.tab {
	case tabHosts:
		if r := a.hosts.current(); r != nil {
			var loc sshconfig.Location
			if r.host != nil {
				loc = r.host.Location
			} else {
				loc = r.rule.Location
			}
			return fmt.Sprintf("%s:%d", config.TildePath(loc.File.Path), loc.LineNo())
		}
		return config.TildePath(a.ssh.Path)
	case tabSettings:
		return config.TildePath(a.cfgPath)
	}
	return ""
}

// barActivity shows the latest message, or what quick-ssh is busy with.
func (a *App) barActivity() segment {
	st := a.st
	if a.toast.text != "" {
		bg := [...]color.Color{st.accent, st.green, st.red}[a.toast.kind]
		icon := [...]string{"•", "✓", "✗"}[a.toast.kind]
		return seg(bg, part{text: icon + " " + a.toast.text, fg: st.accentFg, bold: true})
	}
	switch {
	case a.resolving():
		return seg(st.barMid, part{text: a.spin.View(), fg: st.accent}, part{text: " reading config", fg: st.text})
	case len(a.checks.probing) > 0:
		return seg(st.barMid, part{text: a.spin.View(), fg: st.accent}, part{text: fmt.Sprintf(" checking %d %s", len(a.checks.probing), plural(len(a.checks.probing), "login", "logins")), fg: st.text})
	}
	return segment{}
}

// barSummary counts hosts up and down and the selection, and warns about
// Includes that couldn't be read.
func (a *App) barSummary() segment {
	st := a.st
	var parts []part
	if a.tab == tabHosts {
		if up, down := a.counts(); up+down > 0 {
			parts = append(parts, part{text: fmt.Sprintf("●%d up", up), fg: st.green})
			if down > 0 {
				parts = append(parts, part{text: fmt.Sprintf(" ○%d down", down), fg: st.red})
			}
		}
	}
	if n := len(a.hosts.selected); n > 0 {
		if len(parts) > 0 {
			parts = append(parts, part{text: " ", fg: st.text})
		}
		parts = append(parts, part{text: fmt.Sprintf("✓%d selected", n), fg: st.green})
	}
	if n := len(a.ssh.Warnings); n > 0 {
		if len(parts) > 0 {
			parts = append(parts, part{text: " ", fg: st.text})
		}
		parts = append(parts, part{text: fmt.Sprintf("!%d unreadable %s", n, plural(n, "include", "includes")), fg: st.yellow})
	}
	if len(parts) == 0 {
		return segment{}
	}
	return seg(st.barLow, parts...)
}

func (a *App) barPosition() segment {
	if a.tab != tabHosts || len(a.hosts.view) == 0 {
		return segment{}
	}
	return seg(a.st.barMid, part{text: fmt.Sprintf("%d/%d", a.hosts.cursor+1, len(a.hosts.view)), fg: a.st.text, bold: true})
}

package ui

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/sshconfig"
)

// Below this width the pane can't sit beside the list; d then shows it
// full screen instead.
const sidePaneMinWidth = 100

type detailLayout int

const (
	layoutNone detailLayout = iota
	layoutSide
	layoutFull
)

type detailState struct {
	show bool // side pane enabled (wide terminals)
	full bool // full-screen pane (narrow terminals)

	// Scrolling. scroll resets when a different row is selected
	// (scrollKey). maxScroll and bodyH are recorded at render time for the
	// key handlers.
	scroll    int
	scrollKey string
	maxScroll int
	bodyH     int
}

func (a *App) detailLayout() detailLayout {
	switch {
	case a.tab != tabHosts:
		return layoutNone
	case a.w >= sidePaneMinWidth && a.detail.show:
		return layoutSide
	case a.w < sidePaneMinWidth && a.detail.full:
		return layoutFull
	}
	return layoutNone
}

func (a *App) toggleDetails() {
	if a.w >= sidePaneMinWidth {
		a.detail.show = !a.detail.show
	} else {
		a.detail.full = !a.detail.full
	}
}

// scrollDetail moves the pane's scroll position by delta lines.
func (a *App) scrollDetail(delta int) {
	h := a.h - chromeLines
	switch a.detailLayout() {
	case layoutSide:
		a.renderDetail(a.paneWidth(), h, true)
	case layoutFull:
		a.renderDetail(a.w, h, false)
	}
	a.detail.scroll = max(0, min(a.detail.maxScroll, a.detail.scroll+delta))
}

// detailScrollKey handles pane scrolling keys. In the full-screen layout
// the plain movement keys scroll too, since there is no list to move.
func (a *App) detailScrollKey(key string) bool {
	layout := a.detailLayout()
	if layout == layoutNone {
		return false
	}
	half := max(1, a.detail.bodyH/2)
	switch key {
	case "J":
		a.scrollDetail(1)
	case "K":
		a.scrollDetail(-1)
	case "ctrl+d":
		a.scrollDetail(half)
	case "ctrl+u":
		a.scrollDetail(-half)
	default:
		if layout != layoutFull {
			return false
		}
		switch key {
		case "j", "down":
			a.scrollDetail(1)
		case "k", "up":
			a.scrollDetail(-1)
		case "pgdown":
			a.scrollDetail(a.detail.bodyH)
		case "pgup":
			a.scrollDetail(-a.detail.bodyH)
		default:
			return false
		}
	}
	return true
}

func (a *App) paneWidth() int { return max(40, min(70, a.w*2/5)) }

func (a *App) renderHostsBody(h int) string {
	switch a.detailLayout() {
	case layoutSide:
		pw := a.paneWidth()
		lw := a.w - pw
		return lipgloss.JoinHorizontal(lipgloss.Top,
			padLines(a.renderHosts(lw, h), h, lw),
			padLines(a.renderDetail(pw, h, true), h, pw))
	case layoutFull:
		return a.renderDetail(a.w, h, false)
	}
	return a.renderHosts(a.w, h)
}

func (a *App) renderDetail(w, h int, border bool) string {
	st := a.st
	r := a.hosts.current()
	cw := w - 3 // content width after the border/gutter

	// The header (name, target) stays put; the body scrolls.
	var header, body []string
	lines := &header
	add := func(s string) { *lines = append(*lines, fit(s, cw)) }
	section := func(title string) {
		*lines = append(*lines, "")
		add(st.colHead.Render(title))
	}

	title := ""
	switch {
	case r == nil:
		add(st.dim.Render("No host selected"))
	case r.rule != nil:
		title = r.rule.Text()
		add(st.selName.Render(title))
		add(st.dim.Render("Applies to every host it matches, for options not set earlier."))
		lines = &body
		a.appendSource(r.rule.Location, nil, add, section)
	default:
		h := r.host
		title = h.Alias
		add(st.selName.Render(title))
		res := a.res(h.Alias)
		switch {
		case res == nil:
			add(a.spin.View() + st.dim.Render(" asking ssh…"))
		case res.Err != nil:
			add(st.bad.Render(res.Err.Error()))
		default:
			add(a.targetCell(h))
		}
		lines = &body
		if res != nil && res.Err == nil {
			a.appendConnection(h, res, add, section)
		}
		a.appendHistory(h, add, section)
		a.appendSource(h.Location, h, add, section)
	}

	key := ""
	if r != nil {
		key = r.key()
	}
	if key != a.detail.scrollKey {
		a.detail.scrollKey, a.detail.scroll = key, 0
	}
	bodyH := max(0, h-len(header))
	a.detail.bodyH = bodyH
	a.detail.maxScroll = max(0, len(body)-bodyH)
	a.detail.scroll = min(a.detail.scroll, a.detail.maxScroll)
	if a.detail.maxScroll > 0 && len(header) > 0 {
		// Scroll position on the title line, e.g. "J/K ↕ 40%".
		pct := a.detail.scroll * 100 / a.detail.maxScroll
		ind := st.dim.Render(fmt.Sprintf("J/K ↕ %d%%", pct))
		header[0] = fit(st.selName.Render(title), max(1, cw-lipgloss.Width(ind)-1)) + " " + ind
	}
	all := append(header, body[a.detail.scroll:]...)
	if len(all) > h {
		all = all[:h]
	}
	return a.framePane(all, h, cw, border)
}

// framePane pads the pane to h lines and adds the left border or gutter.
func (a *App) framePane(lines []string, h, cw int, border bool) string {
	prefix := " "
	if border {
		prefix = a.st.rule.Render("│") + " "
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = prefix + fit(l, cw)
	}
	return strings.Join(lines, "\n")
}

// source says where a value comes from when it isn't the host's own
// block: "Host *", another block for the same host, or ssh's default.
func (a *App) source(h *sshconfig.Host, keyword string) string {
	loc, ok := a.ssh.Source(h, keyword)
	switch {
	case !ok:
		return "default"
	case loc == h.Location:
		return ""
	case slices.Contains(h.Also, loc):
		if loc.File == h.File {
			return fmt.Sprintf("from line %d", loc.LineNo())
		}
		return fmt.Sprintf("from %s:%d", config.TildePath(loc.File.Path), loc.LineNo())
	}
	return "from " + loc.Text()
}

// appendConnection lists what ssh will use, and where each value comes
// from when it isn't the host's own block.
func (a *App) appendConnection(h *sshconfig.Host, res *resolve.Resolved, add func(string), section func(string)) {
	st := a.st
	section("CONNECTION")
	field := func(label, value, keyword string) {
		line := st.dim.Render(fmt.Sprintf("%-10s", label)) + " " + st.textS.Render(value)
		if src := a.source(h, keyword); src != "" {
			line += st.faintText.Render("  " + src)
		}
		add(line)
	}
	field("hostname", res.HostName, "hostname")
	field("user", res.User, "user")
	field("port", strconv.Itoa(res.Port), "port")
	if res.ProxyJump != "" {
		field("jump", res.ProxyJump, "proxyjump")
	}
	if res.ProxyCommand != "" {
		field("proxy", res.ProxyCommand, "proxycommand")
	}

	// ssh lists every default key file; only the ones that exist matter.
	var keys []string
	for _, f := range res.IdentityFiles {
		if _, err := os.Stat(config.ExpandPath(f)); err == nil {
			keys = append(keys, f)
		}
	}
	src := a.source(h, "identityfile")
	switch {
	case len(keys) == 0 && src != "default":
		add(st.dim.Render(fmt.Sprintf("%-10s", "key")) + " " + st.warn.Render(strings.Join(res.IdentityFiles, ", ")+" (not found)"))
	case len(keys) == 0:
		add(st.dim.Render(fmt.Sprintf("%-10s", "key")) + " " + st.faintText.Render("none on disk: agent keys only"))
	default:
		for i, k := range keys {
			label := ""
			if i == 0 {
				label = "key"
			}
			line := st.dim.Render(fmt.Sprintf("%-10s", label)) + " " + st.textS.Render(k)
			if i == 0 && src != "" {
				line += st.faintText.Render("  " + src)
			}
			add(line)
		}
	}

	for _, kw := range []string{"localforward", "remoteforward", "dynamicforward"} {
		for _, v := range res.Options[kw] {
			add(st.dim.Render(fmt.Sprintf("%-10s", strings.TrimSuffix(kw, "forward")+" fwd")) + " " + st.textS.Render(v))
		}
	}
}

func (a *App) appendHistory(h *sshconfig.Host, add func(string), section func(string)) {
	st := a.st
	section("HISTORY")
	e := a.hist.Get(h.Alias)
	if e == nil {
		add(st.faintText.Render("never connected from quick-ssh"))
		return
	}
	add(st.agoStyle(e.Last).Render("last "+ago(e.Last)) + st.dim.Render(fmt.Sprintf(" · %d %s", e.Count, plural(e.Count, "connection", "connections"))))
}

// appendSource shows where the block is and the block itself.
func (a *App) appendSource(loc sshconfig.Location, h *sshconfig.Host, add func(string), section func(string)) {
	st := a.st
	section("DEFINED IN")
	add(st.textS.Render(fmt.Sprintf("%s:%d", config.TildePath(loc.File.Path), loc.LineNo())) + st.faintText.Render("  E edits"))
	if h != nil {
		if len(h.Siblings) > 0 {
			add(st.dim.Render("shares its block with " + strings.Join(h.Siblings, ", ")))
		}
		for _, also := range h.Also {
			add(st.dim.Render(fmt.Sprintf("also %s:%d", config.TildePath(also.File.Path), also.LineNo())))
		}
	}
	section("BLOCK")
	for _, l := range trimBlank(loc.Lines()) {
		add(st.dim.Render(strings.ReplaceAll(l.Text, "\t", "    ")))
	}
}

// trimBlank drops blank and comment lines from the end of a block: they
// usually belong to the next one.
func trimBlank(lines []sshconfig.Line) []sshconfig.Line {
	for len(lines) > 1 && lines[len(lines)-1].Blank() {
		lines = lines[:len(lines)-1]
	}
	return lines
}

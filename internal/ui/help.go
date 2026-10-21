package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/bferg314/quick-ssh/internal/config"
)

// helpKey scrolls the help when it's taller than the screen; any other
// key closes it.
func (a *App) helpKey(key string) {
	switch key {
	case "j", "down":
		a.helpScroll++
	case "k", "up":
		a.helpScroll = max(0, a.helpScroll-1)
	case "pgdown", "space":
		a.helpScroll += max(1, a.h/2)
	case "pgup":
		a.helpScroll = max(0, a.helpScroll-max(1, a.h/2))
	default:
		a.help = false
	}
}

// renderHelp is the full key reference: the status bar keeps the screen
// clean, so everything lives here behind "?".
func (a *App) renderHelp() string {
	st := a.st
	// The box's border and padding take 6 columns.
	const keyW = 11
	descW := max(10, min(36, a.w-6-keyW))
	row := func(k, d string) string { return fit(st.key.Render(k), keyW) + fit(st.textS.Render(d), descW) }
	title := func(t string) string { return fit(st.boxTitle.Render(t), keyW+descW) }

	left := []string{
		title("Everywhere"),
		row("tab / ←→", "switch tabs (or 1-4)"),
		row("↑↓ / j k", "move"),
		row("pgup/pgdn", "page"),
		row("/", "filter (on Hosts, enter connects to one)"),
		row("/#tag", "filter by tag"),
		row("?", "this help"),
		row("q", "quit"),
		"",
		title("Hosts"),
		row("enter", "connect (selected hosts: one tab each)"),
		row("m", "everything you can do to the host"),
		row("space / a", "select · select all"),
		row("y", "copy the ssh command, hostname…"),
		row("n", "new host"),
		row("e", "edit the host, or delete it"),
		row("E", "edit its block in your editor"),
		row("d", "show / hide the detail pane"),
		row("J / K", "scroll details (ctrl+d/u: half page)"),
		row("t", "check login (fixes changed host keys)"),
		row("C", "copy one of your keys to the host"),
		row("w", "open the host's web page"),
		row("s", "sort: most used · name · file · status"),
		row("* / h", "pin to the top · hide"),
		row("H", "show / hide hidden hosts"),
		row("r", "reload the config and re-check hosts"),
		"",
		title("Discover"),
		row("enter", "add the machine (selected: all of them)"),
		row("space / a", "select · select all"),
		row("w", "open it in Tailscale's admin console"),
		row("y", "copy its address"),
		row("h / H", "hide · show hidden"),
		row("r", "reload known_hosts and Tailscale"),
		"",
		title("Keys"),
		row("n", "new key (ssh-keygen)"),
		row("C", "copy the key to hosts"),
		row("A", "add it to the agent"),
		row("y", "copy the public key"),
		row("enter", "everything you can do to it"),
	}

	right := []string{title("Tools")}
	tools := a.enabledTools()
	for _, t := range tools {
		k := t.Key
		if reservedKeys[k] {
			k = "(" + k + ")"
		}
		right = append(right, row(k, t.Name))
	}
	if len(tools) == 0 {
		right = append(right, fit(st.dim.Render("none enabled: see Settings"), keyW+descW))
	}
	right = append(right,
		"",
		title("Settings"),
		row("space", "toggle"),
		row("enter", "change · open"),
		"",
		title("From your shell"),
		row(a.cfg.AliasName, "this dashboard"),
		row(a.cfg.AliasName+" <host>", "connect (unique fuzzy match)"),
		row(a.cfg.AliasName+" -", "reconnect to the last host"),
		row(a.cfg.AliasName+" ls", "list hosts (--json, --names)"),
		"",
		title("Status"),
		fit(st.ok.Render("●")+st.textS.Render(" up  ")+st.bad.Render("○")+st.textS.Render(" down  ")+st.dim.Render("○ off")+st.textS.Render(" tailnet  ")+st.warn.Render("▲")+st.textS.Render(" host key"), keyW+descW),
		"",
		title("Marks"),
		fit(st.pin.Render("★")+st.textS.Render(" pinned  ")+st.ok.Render("✓")+st.textS.Render(" selected  ")+
			st.dim.Render("⊘")+st.textS.Render(" hidden"), keyW+descW),
	)

	var lines []string
	if a.w >= 2*(keyW+descW)+12 {
		for len(right) < len(left) {
			right = append(right, "")
		}
		lines = strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(left, "\n"), "    ", strings.Join(right, "\n")), "\n")
	} else {
		lines = append(append(left, ""), right...)
	}
	lines = append(lines, "", st.dim.Render("Tools, keys and options: ")+st.textS.Render(config.TildePath(a.cfgPath)))

	// Scroll when the screen is shorter than the help (border: 2 lines).
	if room := a.h - 2; len(lines) > room && room > 1 {
		a.helpScroll = min(a.helpScroll, len(lines)-(room-1))
		lines = append(lines[a.helpScroll:a.helpScroll+room-1], st.dim.Render("j/k scroll · any other key closes"))
	} else {
		a.helpScroll = 0
	}
	return a.boxed(st.box.Padding(0, 2), lines)
}

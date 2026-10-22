package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/launcher"
)

type settingsTab struct {
	cursor int
}

type settingItem struct {
	section string
	label   string
	detail  string
	on      *bool
	warn    string
	tool    *config.Tool // set for tool rows
	refresh bool         // changing it re-checks the hosts

	// Action rows have no checkbox (on == nil): enter/space runs set.
	set func() tea.Cmd
}

var connectModeText = map[string]string{
	config.ConnectAuto:    "auto: zellij tab, else Windows Terminal tab, else this terminal",
	config.ConnectInPlace: "in this terminal",
	config.ConnectZellij:  "in a new zellij tab",
	config.ConnectWT:      "in a new Windows Terminal tab",
	config.ConnectShell:   "quit and run ssh from your shell (lands in its history)",
}

func (a *App) settingItems() []settingItem {
	sshCfg := settingItem{section: "General", label: "SSH config",
		detail: config.TildePath(a.cfg.SSHConfigPath()) + " · enter edits",
		set:    func() tea.Cmd { return a.editFile(a.cfg.SSHConfigPath(), 1) }}

	mode := settingItem{section: "General", label: "Connect", detail: connectModeText[a.cfg.ConnectMode] + " · enter changes",
		set: func() tea.Cmd {
			i := slices.Index(config.ConnectModes, a.cfg.ConnectMode)
			a.cfg.ConnectMode = config.ConnectModes[(i+1)%len(config.ConnectModes)]
			return a.saveConfig()
		}}
	switch {
	case a.cfg.ConnectMode == config.ConnectZellij && !launcher.InZellij():
		mode.warn = "not in zellij now"
	case a.cfg.ConnectMode == config.ConnectWT && !launcher.InWindowsTerminal():
		mode.warn = "not in Windows Terminal now"
	case a.cfg.ConnectMode == config.ConnectShell && a.cmdFile == "":
		mode.warn = "needs the shell integration"
	}

	stay := settingItem{section: "General", label: "Stay open",
		detail: "return to the list when a session in this terminal ends", on: &a.cfg.StayOpen}

	shellInt := settingItem{section: "General", label: "Shell integration"}
	if a.cmdFile != "" {
		shellInt.detail = a.cfg.AliasName + " is set up in this shell"
	} else {
		shellInt.detail = "run qissh init <shell> for " + a.cfg.AliasName + " and host completion (see README)"
		shellInt.warn = "not set up"
	}

	interval := settingItem{section: "Status", label: "Check every", detail: intervalText(a.cfg.CheckInterval) + " · enter changes",
		set: func() tea.Cmd {
			i := slices.Index(config.CheckIntervals, a.cfg.CheckInterval)
			a.cfg.CheckInterval = config.CheckIntervals[(i+1)%len(config.CheckIntervals)]
			return tea.Batch(a.saveConfig(), a.checkTick())
		}}
	ts := settingItem{section: "Status", label: "Tailscale", on: &a.cfg.Tailscale,
		detail: "online state and last seen for hosts on your tailnet", refresh: true}
	switch {
	case !available("tailscale"):
		ts.warn = "not installed"
	case a.checks.tsErr != nil:
		ts.warn = a.checks.tsErr.Error()
	case a.cfg.Tailscale:
		ts.detail += fmt.Sprintf(" · %d machines", len(a.checks.peers))
	}
	items := []settingItem{sshCfg, mode, stay, shellInt}
	items = append(items, interval, ts)
	for i := range a.cfg.Tools {
		t := &a.cfg.Tools[i]
		it := settingItem{
			section: "Tools",
			label:   t.Name,
			detail:  "[" + t.Key + "]  " + strings.TrimSpace(t.Cmd+" "+strings.Join(t.Args, " ")) + "  · " + t.Mode,
			on:      &t.Enabled,
			tool:    t,
		}
		switch {
		case !available(t.Cmd):
			it.warn = "not installed"
		case reservedKeys[t.Key]:
			it.warn = "key " + t.Key + " is reserved"
		}
		items = append(items, it)
	}
	items = append(items,
		settingItem{section: "Appearance", label: "Powerline status bar", detail: "arrow separators; needs a Nerd Font", on: &a.cfg.Powerline},
		settingItem{section: "Appearance", label: "Mouse", detail: "click, double-click to connect, scroll; Shift+drag selects text", on: &a.cfg.Mouse},
	)
	return items
}

func (a *App) settingsKey(key string) tea.Cmd {
	items := a.settingItems()
	t := &a.settings
	switch key {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = min(len(items)-1, t.cursor+1)
	case "space", "enter":
		it := items[t.cursor]
		if it.on == nil {
			if it.set != nil {
				return it.set()
			}
			return nil
		}
		*it.on = !*it.on
		if it.tool != nil {
			it.tool.AutoDisabled = false // a hand-made choice sticks
		}
		if it.refresh {
			return tea.Batch(a.saveConfig(), a.recheck())
		}
		return a.saveConfig()
	}
	return nil
}

func (a *App) renderSettings(h int) string {
	st := a.st
	items := a.settingItems()
	var lines []string
	var itemOf []int // the item on each line; -1 for the rest
	cursorLine := 0
	section := ""
	for i, it := range items {
		if it.section != section {
			section = it.section
			if len(lines) > 0 {
				lines, itemOf = append(lines, ""), append(itemOf, -1)
			}
			lines, itemOf = append(lines, "  "+st.boxTitle.Render(section)), append(itemOf, -1)
		}
		itemOf = append(itemOf, i)
		marker := "  "
		label := st.textS.Render(it.label)
		if i == a.settings.cursor {
			cursorLine = len(lines)
			marker = st.marker.Render("▌") + " "
			label = st.selName.Render(it.label)
		}
		var box string
		switch {
		case it.on == nil && it.set == nil:
			box = "   "
		case it.on == nil:
			box = st.key.Render(" ▸ ")
		case *it.on:
			box = st.ok.Render("[✓]")
		default:
			box = st.faintText.Render("[ ]")
		}
		line := marker + "  " + box + " " + fit(label, 22)
		if it.warn != "" {
			line += st.warn.Render(it.warn) + "  "
		}
		lines = append(lines, line+st.dim.Render(it.detail))
	}
	lines = append(lines, "",
		"    "+st.dim.Render("Add tools, change keys and the ssh config path in the config file below."))

	// Scroll so the cursor (and its section heading) stay visible.
	start := 0
	if len(lines) > h {
		start = max(0, min(cursorLine-h/2, len(lines)-h))
		lines = lines[start : start+h]
	}

	// A click picks a row; on its checkbox, or a double click, it's enter.
	a.addZone(zone{w: a.w, h: h, wheel: wheelKeys(a.settingsKey)})
	for y := range lines {
		if y+start >= len(itemOf) || itemOf[y+start] < 0 {
			continue
		}
		i := itemOf[y+start]
		pick := func() { a.settings.cursor = i }
		enter := func() tea.Cmd { pick(); return a.settingsKey("enter") }
		a.addZone(zone{y: y, w: a.w, h: 1, click: func() tea.Cmd { pick(); return nil }, dbl: enter})
		a.addZone(zone{x: 4, y: y, w: 3, h: 1, click: enter}) // after the marker: [✓] or ▸
	}
	return strings.Join(lines, "\n")
}

func intervalText(s string) string {
	if s == "0" {
		return "only at start and on r"
	}
	return "every " + s + " while qissh is open"
}

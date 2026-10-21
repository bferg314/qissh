package ui

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/run"
)

// actionPopup lists what can be done to the selected host. Each action
// can be picked with the cursor or by its key.
type actionPopup struct {
	title   string
	actions []popupAction
	cursor  int
}

type popupAction struct {
	key   string
	label string
	note  string // dimmed, after the label
	run   func() tea.Cmd
}

// openActions shows everything that can be done to the host under the
// cursor (m).
func (a *App) openActions() tea.Cmd {
	r := a.hosts.current()
	if r == nil {
		return nil
	}
	if r.rule != nil {
		return a.edit(r.rule.Location)
	}
	h := r.host
	p := &actionPopup{title: h.Alias}
	add := func(key, label, note string, run func() tea.Cmd) {
		p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: run})
	}
	mode := a.connectMode()
	note := map[string]string{
		config.ConnectInPlace: "in this terminal",
		config.ConnectZellij:  "in a new zellij tab",
		config.ConnectWT:      "in a new Windows Terminal tab",
		config.ConnectShell:   "from your shell",
	}[mode]
	add("enter", "Connect", note, func() tea.Cmd { return a.connect(h.Alias) })
	for _, t := range a.enabledTools() {
		if reservedKeys[t.Key] {
			continue
		}
		add(t.Key, t.Name, strings.Join(append([]string{t.Cmd}, t.Args...), " "), func() tea.Cmd { return a.launch(t, h.Alias) })
	}
	add("y", "Copy…", "ssh command, hostname, block", a.copyCurrent)
	add("E", "Edit in editor", config.TildePath(h.File.Path), func() tea.Cmd { return a.edit(h.Location) })
	pinned, hidden := a.cfg.HostMarks()
	if pinned[config.HostKey(h.Alias)] {
		add("*", "Unpin", "", a.togglePinned)
	} else {
		add("*", "Pin to the top", "", a.togglePinned)
	}
	if hidden[config.HostKey(h.Alias)] {
		add("h", "Unhide", "", a.toggleHidden)
	} else {
		add("h", "Hide", "", a.toggleHidden)
	}
	a.popup = p
	return nil
}

func (a *App) popupKey(key string) tea.Cmd {
	p := a.popup
	switch key {
	case "esc", "q":
		a.popup = nil
	case "up", "k":
		p.cursor = (p.cursor + len(p.actions) - 1) % len(p.actions)
	case "down", "j":
		p.cursor = (p.cursor + 1) % len(p.actions)
	case "enter":
		a.popup = nil
		return p.actions[p.cursor].run()
	default:
		for _, act := range p.actions {
			if act.key == key {
				a.popup = nil
				return act.run()
			}
		}
	}
	return nil
}

func (a *App) renderPopup() string {
	st := a.st
	p := a.popup
	keyW := 1
	for _, act := range p.actions {
		keyW = max(keyW, lipgloss.Width(act.key))
	}
	lines := []string{st.bold.Render(p.title), ""}
	for i, act := range p.actions {
		marker := "  "
		label := st.textS.Render(act.label)
		if i == p.cursor {
			marker = st.marker.Render("▌ ")
			label = st.selName.Render(act.label)
		}
		line := marker + fit(st.key.Render(act.key), keyW) + "  " + fit(label, 16)
		if act.note != "" {
			line += "  " + st.dim.Render(act.note)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", st.dim.Render("enter or a key runs it · esc closes"))
	return a.boxed(st.box, lines)
}

// boxed draws lines in a popup box no wider than the terminal: the box's
// border and padding take 6 columns, and longer lines are shortened so
// the border stays whole.
func (a *App) boxed(box lipgloss.Style, lines []string) string {
	w := max(10, a.w-6)
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = ansi.Truncate(l, w, "…")
		}
	}
	return box.Render(strings.Join(lines, "\n"))
}

func firstLine(s string) string { return run.FirstLine(s) }

// quoteArg quotes an argument for a shell when it needs it.
func quoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t'\"$&|;<>()*?") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return filepath.ToSlash(s)
}

func joinArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = quoteArg(a)
	}
	return strings.Join(out, " ")
}

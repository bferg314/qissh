package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/tunnel"
)

// tunnelEndMsg is a tunnel's ssh exiting; err is nil when it was stopped.
type tunnelEndMsg struct {
	alias string
	spec  tunnel.Spec
	err   error
}

// waitTunnel delivers the next tunnel that ends. It's re-armed after
// each one.
func (a *App) waitTunnel() tea.Cmd {
	ch := a.tunnelEnded
	return func() tea.Msg { return <-ch }
}

func (a *App) handleTunnelEnd(m tunnelEndMsg) tea.Cmd {
	cmds := []tea.Cmd{a.waitTunnel()}
	if m.err != nil {
		cmds = append(cmds, a.notify(2, "Tunnel %s on %s stopped: %v", m.spec, m.alias, m.err))
	}
	return tea.Batch(cmds...)
}

// StopTunnels ends every tunnel; main calls it when the program exits.
func (a *App) StopTunnels() { a.tunnels.StopAll() }

// savedTunnels are alias's saved forwards that parse.
func (a *App) savedTunnels(alias string) []savedTunnel {
	var out []savedTunnel
	for _, t := range a.cfg.Meta(alias).Tunnels {
		if s, err := tunnel.Parse(t.Spec); err == nil {
			out = append(out, savedTunnel{name: t.Name, spec: s})
		}
	}
	return out
}

type savedTunnel struct {
	name string
	spec tunnel.Spec
}

func (t savedTunnel) label() string {
	if t.name != "" {
		return t.name
	}
	return t.spec.String()
}

// tunnelMenu (T) lists the host's saved and running forwards, each
// started or stopped with its number, and offers a new one.
func (a *App) tunnelMenu() tea.Cmd {
	h := a.hosts.currentHost()
	if h == nil {
		return nil
	}
	alias := h.Alias
	p := &actionPopup{title: "Tunnels to " + alias}
	saved := a.savedTunnels(alias)
	n := 0
	seen := map[string]bool{}
	for _, t := range saved {
		seen[t.spec.String()] = true
		n++
		a.addTunnelAction(p, strconv.Itoa(n%10), alias, t.label(), t.spec)
	}
	for _, t := range a.tunnels.List() {
		if strings.EqualFold(t.Alias, alias) && !seen[t.Spec.String()] {
			n++
			a.addTunnelAction(p, strconv.Itoa(n%10), alias, t.Spec.String(), t.Spec)
		}
	}
	if n == 0 {
		p.body = []string{"No saved tunnels for " + alias + " yet."}
	}
	p.actions = append(p.actions, popupAction{key: "n", label: "New tunnel…", note: "5432, 8080:localhost:80, R 9000:localhost:3000, D 1080",
		run: func() tea.Cmd { return a.openTunnelForm(alias) }})
	a.popup = p
	return nil
}

func (a *App) addTunnelAction(p *actionPopup, key, alias, label string, s tunnel.Spec) {
	note := s.Describe()
	if a.tunnels.Running(alias, s) {
		label = "● " + label
		note = "running · stops it"
	}
	if len(p.actions) >= 10 {
		key = ""
	}
	p.actions = append(p.actions, popupAction{key: key, label: label, note: note, run: func() tea.Cmd { return a.toggleTunnel(alias, s) }})
}

// toggleTunnel starts the forward, or stops it if it's running.
func (a *App) toggleTunnel(alias string, s tunnel.Spec) tea.Cmd {
	if a.tunnels.Stop(alias, s) {
		return a.notify(0, "Stopped %s on %s", s, alias)
	}
	return a.startTunnel(alias, "", s)
}

func (a *App) startTunnel(alias, name string, s tunnel.Spec) tea.Cmd {
	ch := a.tunnelEnded
	err := a.tunnels.Start(a.sshOptions(), alias, name, s, func(err error) {
		ch <- tunnelEndMsg{alias: alias, spec: s, err: err}
	})
	if err != nil {
		return a.notify(2, "Tunnel to %s: %v", alias, err)
	}
	return a.notify(1, "%s via %s", s.Describe(), alias)
}

// tunnelForm starts a forward that isn't saved, and can save it.
type tunnelForm struct {
	alias  string
	inputs [2]textinput.Model // the forward, a name
	focus  int
	err    string
}

func (a *App) openTunnelForm(alias string) tea.Cmd {
	f := &tunnelForm{alias: alias}
	for i, ph := range []string{"5432 · 8080:localhost:80 · R 9000:localhost:3000 · D 1080", "optional, e.g. postgres"} {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = ph
		f.inputs[i] = in
	}
	a.tunnelForm = f
	return f.inputs[0].Focus()
}

func (a *App) tunnelFormKey(msg tea.KeyPressMsg) tea.Cmd {
	f := a.tunnelForm
	switch msg.String() {
	case "esc":
		a.tunnelForm = nil
		return nil
	case "tab", "down", "shift+tab", "up":
		f.inputs[f.focus].Blur()
		f.focus = 1 - f.focus
		return f.inputs[f.focus].Focus()
	case "enter", "ctrl+s":
		s, err := tunnel.Parse(f.inputs[0].Value())
		if err != nil {
			f.err = err.Error()
			return nil
		}
		name := strings.TrimSpace(f.inputs[1].Value())
		a.tunnelForm = nil
		cmds := []tea.Cmd{a.startTunnel(f.alias, name, s)}
		if msg.String() == "ctrl+s" {
			m := a.cfg.Meta(f.alias)
			m.Tunnels = append(m.Tunnels, config.Tunnel{Name: name, Spec: s.String()})
			a.cfg.SetMeta(f.alias, m)
			cmds = append(cmds, a.saveConfig())
		}
		return tea.Batch(cmds...)
	}
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
	f.err = ""
	return cmd
}

func (a *App) renderTunnelForm() string {
	st := a.st
	f := a.tunnelForm
	w := max(16, min(56, a.w-6-10))
	lines := []string{st.bold.Render("New tunnel to " + f.alias), ""}
	for i, label := range []string{"Forward", "Name"} {
		in := f.inputs[i]
		in.SetWidth(w)
		marker, lab := "  ", st.dim.Render(label)
		if i == f.focus {
			marker, lab = st.marker.Render("▌ "), st.selName.Render(label)
		}
		lines = append(lines, marker+fit(lab, 9)+in.View())
	}
	lines = append(lines, "")
	if s, err := tunnel.Parse(f.inputs[0].Value()); err == nil {
		lines = append(lines, st.target.Render(s.Describe()))
	} else {
		lines = append(lines, st.dim.Render("A port forwards it from the host to here."))
	}
	switch {
	case f.err != "":
		lines = append(lines, st.bad.Render(f.err))
	default:
		lines = append(lines, st.dim.Render("enter starts · ctrl+s starts and saves to "+f.alias+" · esc cancels"))
	}
	return a.boxed(st.box, lines)
}

// appendTunnels lists the host's saved and running forwards in the
// detail pane.
func (a *App) appendTunnels(alias string, add func(string), section func(string)) {
	st := a.st
	saved := a.savedTunnels(alias)
	var running []tunnel.Tunnel
	for _, t := range a.tunnels.List() {
		if strings.EqualFold(t.Alias, alias) {
			running = append(running, t)
		}
	}
	if len(saved) == 0 && len(running) == 0 {
		return
	}
	section("TUNNELS")
	shown := map[string]bool{}
	line := func(label string, s tunnel.Spec) {
		shown[s.String()] = true
		if t := runningTunnel(running, s); t != nil {
			add(st.ok.Render("● ") + st.textS.Render(label) + st.dim.Render("  "+s.Describe()+" · up "+strings.TrimSuffix(ago(t.Started), " ago")))
		} else {
			add(st.faintText.Render("○ ") + st.textS.Render(label) + st.dim.Render("  "+s.Describe()))
		}
	}
	for _, t := range saved {
		line(t.label(), t.spec)
	}
	for _, t := range running {
		if !shown[t.Spec.String()] {
			line(t.Spec.String(), t.Spec)
		}
	}
	add(st.faintText.Render("T starts and stops them"))
}

func runningTunnel(ts []tunnel.Tunnel, s tunnel.Spec) *tunnel.Tunnel {
	for i := range ts {
		if ts[i].Spec == s {
			return &ts[i]
		}
	}
	return nil
}

// tunnelSummary is the status bar's count of running tunnels.
func (a *App) tunnelSummary() string {
	n := len(a.tunnels.List())
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("⇄%d %s", n, plural(n, "tunnel", "tunnels"))
}

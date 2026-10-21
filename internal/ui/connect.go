package ui

import (
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/launcher"
	"github.com/bferg314/quick-ssh/internal/sshconfig"
)

// SSHArgv is the command that connects to alias: plain `ssh alias`, plus
// -F when quick-ssh is pointed at another config file.
func SSHArgv(cfg *config.Config, alias string) []string {
	var args []string
	if cfg.CustomSSHConfig() {
		args = append(args, "-F", cfg.SSHConfigPath())
	}
	return append(append([]string{"ssh"}, args...), alias)
}

// connectMode is where sessions open now: auto picks zellij, then Windows
// Terminal, then this terminal.
func (a *App) connectMode() string {
	if a.cfg.ConnectMode != config.ConnectAuto {
		return a.cfg.ConnectMode
	}
	switch {
	case launcher.InZellij():
		return config.ConnectZellij
	case launcher.InWindowsTerminal():
		return config.ConnectWT
	}
	return config.ConnectInPlace
}

// connect opens a session to each alias.
func (a *App) connect(aliases ...string) tea.Cmd {
	if len(aliases) == 0 {
		return nil
	}
	mode := a.connectMode()
	switch mode {
	case config.ConnectZellij:
		if !launcher.InZellij() {
			return a.notify(2, "connect_mode is zellij, but quick-ssh isn't running inside zellij")
		}
	case config.ConnectWT:
		if !launcher.InWindowsTerminal() {
			return a.notify(2, "connect_mode is wt, but quick-ssh isn't running inside Windows Terminal")
		}
	default:
		if len(aliases) > 1 {
			return a.notify(2, "Opening several hosts at once needs zellij or Windows Terminal tabs")
		}
	}

	for _, alias := range aliases {
		_ = a.hist.Record(alias)
	}
	a.hosts.clearSelection()

	switch mode {
	case config.ConnectZellij, config.ConnectWT:
		var cmds []tea.Cmd
		for _, alias := range aliases {
			argv := SSHArgv(a.cfg, alias)
			var cmd *exec.Cmd
			if mode == config.ConnectZellij {
				cmd = launcher.ZellijTab(alias, argv)
			} else {
				cmd = launcher.WTTab(alias, argv)
			}
			cmds = append(cmds, func() tea.Msg {
				out, err := cmd.CombinedOutput()
				if err != nil {
					if line := firstLine(string(out)); line != "" {
						return tabOpenedMsg{alias: alias, err: errorString(line)}
					}
				}
				return tabOpenedMsg{alias: alias, err: err}
			})
		}
		a.refresh()
		if len(aliases) == 1 {
			cmds = append(cmds, a.notify(1, "Opened %s in a new tab", aliases[0]))
		} else {
			cmds = append(cmds, a.notify(1, "Opened %d hosts in new tabs", len(aliases)))
		}
		return tea.Batch(cmds...)
	}

	argv := SSHArgv(a.cfg, aliases[0])
	if a.cfg.StayOpen && mode == config.ConnectInPlace {
		a.refresh()
		alias := aliases[0]
		return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg {
			return sessionEndMsg{alias: alias, err: err}
		})
	}
	// Quit first and let main run ssh, so the session gets a clean terminal
	// rather than one quick-ssh is about to redraw.
	a.outcome = Outcome{Argv: argv, ViaShell: mode == config.ConnectShell && a.cmdFile != ""}
	return tea.Quit
}

// launch runs tool t for alias.
func (a *App) launch(t config.Tool, alias string) tea.Cmd {
	if !config.Available(t.Cmd) {
		return a.notify(2, "%s: %q not found on PATH", t.Name, t.Cmd)
	}
	v := launcher.Vars{Alias: alias}
	if res := a.res(alias); res != nil && res.Err == nil {
		v.HostName, v.User, v.Port = res.HostName, res.User, res.Port
	}
	cmd := launcher.Command(t, v)
	if t.Mode == config.ModeTerminal {
		if launcher.InZellij() && !t.InPlace {
			tab := launcher.ZellijTab(alias+" · "+t.Name, cmd.Args)
			return tea.Batch(func() tea.Msg {
				out, err := tab.CombinedOutput()
				if err != nil && firstLine(string(out)) != "" {
					err = errorString(firstLine(string(out)))
				}
				return toolExitMsg{name: t.Name, err: err}
			}, a.notify(1, "Opened %s for %s in a new tab", t.Name, alias))
		}
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return toolExitMsg{name: t.Name, err: err} })
	}
	if err := launcher.Detach(cmd); err != nil {
		return a.notify(2, "%s: %v", t.Name, err)
	}
	return a.notify(1, "Opened %s in %s", alias, t.Name)
}

// edit opens the file defining loc in the editor, at its line. The config
// is reloaded when a terminal editor exits; the file watcher catches
// changes made in a detached one.
func (a *App) edit(loc sshconfig.Location) tea.Cmd {
	return a.editFile(loc.File.Path, loc.LineNo())
}

func (a *App) editFile(path string, line int) tea.Cmd {
	t := launcher.Editor(path, line)
	cmd := exec.Command(t.Cmd, t.Args...)
	if t.Mode == config.ModeTerminal {
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorExitMsg{err: err} })
	}
	if err := launcher.Detach(cmd); err != nil {
		return a.notify(2, "%s: %v", t.Name, err)
	}
	return a.notify(1, "Opened %s in %s", config.TildePath(path), t.Name)
}

// editCurrent opens the row under the cursor in the editor, or the main
// config when the list is empty (to add the first host).
func (a *App) editCurrent() tea.Cmd {
	r := a.hosts.current()
	switch {
	case r == nil:
		return a.editFile(a.ssh.Path, 1)
	case r.rule != nil:
		return a.edit(r.rule.Location)
	}
	return a.edit(r.host.Location)
}

// writeClipboard writes the local clipboard; tests replace it.
var writeClipboard = clipboard.WriteAll

// copyText puts s on the clipboard: through the terminal (OSC 52, which
// reaches your own machine even over SSH) and, when not over SSH, the
// local clipboard as well, for terminals that ignore OSC 52.
func (a *App) copyText(what, s string) tea.Cmd {
	if os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == "" {
		_ = writeClipboard(s)
	}
	return tea.Batch(tea.SetClipboard(s), a.notify(1, "Copied %s", what))
}

// copyCurrent offers what can be copied from the host under the cursor.
func (a *App) copyCurrent() tea.Cmd {
	h := a.hosts.currentHost()
	if h == nil {
		return nil
	}
	p := &actionPopup{title: "Copy from " + h.Alias}
	add := func(key, label, value string) {
		if value != "" {
			p.actions = append(p.actions, popupAction{key: key, label: label, note: shorten(value, 48),
				run: func() tea.Cmd { return a.copyText(label, value) }})
		}
	}
	add("c", "ssh command", joinArgs(SSHArgv(a.cfg, h.Alias)))
	add("a", "alias", h.Alias)
	if res := a.res(h.Alias); res != nil && res.Err == nil {
		add("h", "hostname", res.HostName)
		if res.User != "" {
			add("u", "user@hostname", res.User+"@"+res.HostName)
		}
	}
	add("b", "config block", blockText(h.Location))
	a.popup = p
	return nil
}

// blockText is the block as written in the file.
func blockText(loc sshconfig.Location) string {
	var s string
	for _, l := range loc.Lines() {
		s += l.Text + "\n"
	}
	return s
}

type errorString string

func (e errorString) Error() string { return string(e) }

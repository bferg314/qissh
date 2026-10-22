package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/quick-ssh/internal/remote"
)

// runTimeout is how long a command may take on each host.
const runTimeout = 2 * time.Minute

// runView runs a command on several hosts (x): first a prompt with the
// saved snippets, then everyone's output.
type runView struct {
	aliases []string
	input   textinput.Model
	// cursor is the highlighted snippet; -1 is the input.
	cursor int

	// Once running:
	id      int
	command string
	results map[string]*remote.Result // by alias; missing while running
	started time.Time
	scroll  int
}

type runResultMsg struct {
	id  int
	res remote.Result
}

func (v *runView) running() bool { return v.command != "" && len(v.results) < len(v.aliases) }

func (a *App) openRun() tea.Cmd {
	aliases := a.targets()
	if len(aliases) == 0 {
		return nil
	}
	in := textinput.New()
	in.Prompt = "$ "
	in.Placeholder = "a command, e.g. uptime; df -h /"
	a.runv = &runView{aliases: aliases, input: in, cursor: -1}
	return a.runv.input.Focus()
}

func (a *App) runKey(msg tea.KeyPressMsg) tea.Cmd {
	v := a.runv
	key := msg.String()
	if v.command != "" {
		return a.runResultsKey(key)
	}
	snippets := a.cfg.Snippets
	switch key {
	case "esc":
		a.runv = nil
		return nil
	case "up":
		v.cursor = max(-1, v.cursor-1)
	case "down":
		v.cursor = min(len(snippets)-1, v.cursor+1)
	case "ctrl+s":
		cmd := strings.TrimSpace(v.input.Value())
		if cmd == "" {
			return nil
		}
		if !a.cfg.AddSnippet(cmd) {
			return a.notify(0, "Already saved")
		}
		return tea.Batch(a.saveConfig(), a.notify(1, "Saved the snippet"))
	case "ctrl+d":
		if v.cursor >= 0 && v.cursor < len(snippets) {
			a.cfg.Snippets = slices.Delete(slices.Clone(snippets), v.cursor, v.cursor+1)
			v.cursor = min(v.cursor, len(a.cfg.Snippets)-1)
			return tea.Batch(a.saveConfig(), a.notify(0, "Deleted the snippet"))
		}
	case "enter":
		cmd := strings.TrimSpace(v.input.Value())
		if v.cursor >= 0 && v.cursor < len(snippets) {
			cmd = snippets[v.cursor].Cmd
		}
		if cmd == "" {
			return nil
		}
		return a.startRun(cmd)
	default:
		var c tea.Cmd
		v.input, c = v.input.Update(msg)
		v.cursor = -1
		return c
	}
	if v.cursor >= 0 {
		v.input.SetValue(snippets[v.cursor].Cmd)
		v.input.CursorEnd()
	}
	return nil
}

// startRun runs cmd on every host at once.
func (a *App) startRun(cmd string) tea.Cmd {
	v := a.runv
	a.runID++
	v.id, v.command, v.results, v.started, v.scroll = a.runID, cmd, map[string]*remote.Result{}, time.Now(), 0
	v.input.Blur()
	r, opts, id := a.runner, a.sshOptions(), v.id
	cmds := []tea.Cmd{a.startSpinner()}
	for _, alias := range v.aliases {
		cmds = append(cmds, func() tea.Msg {
			return runResultMsg{id: id, res: remote.Run(context.Background(), r, opts, alias, cmd, runTimeout)}
		})
	}
	return tea.Batch(cmds...)
}

func (a *App) handleRunResult(m runResultMsg) tea.Cmd {
	v := a.runv
	if v == nil || m.id != v.id {
		return nil // closed, or run again since
	}
	res := m.res
	v.results[res.Alias] = &res
	if v.running() {
		return nil
	}
	ok := 0
	for _, r := range v.results {
		if r.OK() {
			ok++
		}
	}
	if ok == len(v.aliases) {
		return a.notify(1, "%s: done on %d %s", shorten(v.command, 30), ok, plural(ok, "host", "hosts"))
	}
	return a.notify(2, "%s: failed on %d of %d", shorten(v.command, 30), len(v.aliases)-ok, len(v.aliases))
}

func (a *App) runResultsKey(key string) tea.Cmd {
	v := a.runv
	page := max(1, a.h-8)
	switch key {
	case "esc", "q":
		a.runv = nil
	case "j", "down":
		v.scroll++
	case "k", "up":
		v.scroll = max(0, v.scroll-1)
	case "pgdown", "space":
		v.scroll += page
	case "pgup":
		v.scroll = max(0, v.scroll-page)
	case "home", "g":
		v.scroll = 0
	case "r":
		if !v.running() {
			return a.startRun(v.command)
		}
	case "e":
		// Back to the prompt to change the command.
		if !v.running() {
			v.input.SetValue(v.command)
			v.input.CursorEnd()
			v.command, v.results = "", nil
			return v.input.Focus()
		}
	case "y":
		return a.copyText("the output", a.runText())
	}
	return nil
}

// runOrder is failures first, then hosts still running, then the rest, each
// in the order they were picked.
func (a *App) runOrder() []string {
	v := a.runv
	rank := func(alias string) int {
		r := v.results[alias]
		switch {
		case r == nil:
			return 1
		case !r.OK():
			return 0
		}
		return 2
	}
	out := slices.Clone(v.aliases)
	slices.SortStableFunc(out, func(x, y string) int { return rank(x) - rank(y) })
	return out
}

// runText is everyone's output as plain text, for copying.
func (a *App) runText() string {
	v := a.runv
	var b strings.Builder
	for _, alias := range a.runOrder() {
		r := v.results[alias]
		if r == nil {
			continue
		}
		fmt.Fprintf(&b, "== %s (%s)\n%s\n", alias, runStatus(r), strings.TrimRight(r.Output, "\n"))
	}
	return b.String()
}

func runStatus(r *remote.Result) string {
	switch {
	case r.Err != nil:
		return r.Err.Error()
	case r.Exit != 0:
		return fmt.Sprintf("exit %d", r.Exit)
	}
	return "ok"
}

func (a *App) renderRun() string {
	v := a.runv
	if v.command == "" {
		return a.renderRunPrompt()
	}
	st := a.st
	w, h := max(20, a.w-6), max(3, a.h-4)

	ok, failed := 0, 0
	for _, r := range v.results {
		if r.OK() {
			ok++
		} else {
			failed++
		}
	}
	head := st.bold.Render("$ "+v.command) + st.dim.Render(fmt.Sprintf("  on %d %s", len(v.aliases), plural(len(v.aliases), "host", "hosts")))
	sum := st.ok.Render(fmt.Sprintf("✓ %d ok", ok))
	if failed > 0 {
		sum += "  " + st.bad.Render(fmt.Sprintf("✗ %d failed", failed))
	}
	if n := len(v.aliases) - len(v.results); n > 0 {
		sum += "  " + a.spin.View() + st.dim.Render(fmt.Sprintf(" %d running", n))
	}

	var body []string
	for _, alias := range a.runOrder() {
		r := v.results[alias]
		if len(body) > 0 {
			body = append(body, "")
		}
		switch {
		case r == nil:
			body = append(body, a.spin.View()+" "+st.bold.Render(alias)+st.dim.Render(" running…"))
			continue
		case r.OK():
			body = append(body, st.ok.Render("✓ ")+st.bold.Render(alias)+st.dim.Render(fmt.Sprintf("  %.1fs", r.Duration.Seconds())))
		default:
			body = append(body, st.bad.Render("✗ "+alias)+"  "+st.warn.Render(runStatus(r))+st.dim.Render(fmt.Sprintf("  %.1fs", r.Duration.Seconds())))
		}
		out := strings.TrimRight(strings.ReplaceAll(r.Output, "\r\n", "\n"), "\n")
		if out == "" {
			body = append(body, "  "+st.faintText.Render("(no output)"))
			continue
		}
		for _, l := range strings.Split(out, "\n") {
			body = append(body, "  "+st.textS.Render(strings.ReplaceAll(l, "\t", "    ")))
		}
	}

	foot := st.dim.Render("j/k scroll · r run again · e change the command · y copy · esc close")
	room := max(1, h-4)
	v.scroll = max(0, min(v.scroll, len(body)-room))
	shown := body[v.scroll:min(len(body), v.scroll+room)]
	lines := append([]string{head, sum, ""}, shown...)
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	lines = append(lines, foot)
	for i, l := range lines {
		lines[i] = fit(l, w)
	}
	return a.boxed(st.box, lines)
}

func (a *App) renderRunPrompt() string {
	st := a.st
	v := a.runv
	w := max(20, min(80, a.w-6))
	v.input.SetWidth(w - 2)
	lines := []string{st.bold.Render("Run on " + joinAliases(v.aliases)), st.dim.Render("Without a terminal: commands that prompt (sudo with a password) fail."), "", v.input.View(), ""}
	if len(a.cfg.Snippets) == 0 {
		lines = append(lines, st.faintText.Render("No saved snippets yet: ctrl+s saves the command."))
	} else {
		lines = append(lines, st.colHead.Render("SNIPPETS"))
		for i, s := range a.cfg.Snippets {
			text := s.Cmd
			if s.Name != "" {
				text = s.Name + st.dim.Render("  "+s.Cmd)
			}
			if i == v.cursor {
				lines = append(lines, st.marker.Render("▌ ")+st.selName.Render(ansi.Strip(text)))
			} else {
				lines = append(lines, "  "+st.textS.Render(text))
			}
			if len(lines) > a.h-12 {
				break
			}
		}
	}
	lines = append(lines, "", st.dim.Render("enter runs · ↑↓ snippets · ctrl+s save · ctrl+d delete snippet · esc cancel"))
	return a.boxed(st.box, lines)
}

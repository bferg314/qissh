// Package ui is quick-ssh's Bubble Tea interface.
package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/history"
	"github.com/bferg314/quick-ssh/internal/keys"
	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/run"
	"github.com/bferg314/quick-ssh/internal/sshconfig"
)

const (
	tabHosts = iota
	tabDiscover
	tabKeys
	tabSettings
)

var tabNames = []string{"Hosts", "Discover", "Keys", "Settings"}

// chromeLines is the screen height used by the header, its rule and the
// status bar around the body.
const chromeLines = 3

// resolveWorkers is how many `ssh -G` run at once.
const resolveWorkers = 8

// watchInterval is how often the ssh config files are checked for changes
// made outside quick-ssh.
const watchInterval = 2 * time.Second

// Keys quick-ssh handles itself on the Hosts tab; tools bound to these are
// shadowed.
var reservedKeys = map[string]bool{
	"q": true, "?": true, "/": true, "r": true, "s": true, "m": true, "d": true, "y": true,
	"j": true, "k": true, "J": true, "K": true, "1": true, "2": true, "3": true, "4": true,
	"*": true, "h": true, "H": true, "E": true, "e": true, "n": true, "t": true, "a": true, "space": true, "enter": true,
	"C": true, "w": true,
}

// Options are what main hands the app.
type Options struct {
	Config     *config.Config
	ConfigPath string
	Runner     run.Runner
	History    *history.History
	// Filter starts the Hosts tab filtered, for `qs <query>` with several
	// matches.
	Filter string
	// CmdFile is set when launched through the shell wrapper.
	CmdFile string
	// BackupDir is where config backups go; "" uses the OS cache folder.
	BackupDir string
	// KeyDir is where key pairs are looked for; "" is ~/.ssh.
	KeyDir string
}

// Outcome is what to do once the TUI has exited.
type Outcome struct {
	// Argv is an ssh command to run in this terminal, or nil.
	Argv []string
	// ViaShell hands Argv to the shell wrapper instead of running it.
	ViaShell bool
}

// App is the root model.
type App struct {
	cfg     *config.Config
	cfgPath string
	runner  run.Runner
	hist    *history.History
	cmdFile string
	st      styles

	w, h     int
	tab      int
	hosts    hostsTab
	keys     keysTab
	settings settingsTab
	detail   detailState

	ssh      *sshconfig.Config
	sshErr   error
	resolved map[string]*resolve.Resolved // by lowercase alias
	resGen   int
	sem      chan struct{}
	mtimes   map[string]time.Time
	checks   checks

	spin     spinner.Model
	spinning bool
	popup    *actionPopup
	form     *hostForm
	keyForm  *keyForm
	// copyKey is a key chosen on the Keys tab, waiting for hosts to be
	// picked on the Hosts tab.
	copyKey    *keys.Key
	keyDir     string
	backupDir  string
	help       bool
	helpScroll int
	toast      toast

	outcome Outcome
}

type toast struct {
	id   int
	text string
	kind int // 0 info, 1 ok, 2 error
}

// Messages.
type (
	resolvedMsg struct {
		gen   int
		alias string
		res   *resolve.Resolved
	}
	watchMsg      struct{}
	sessionEndMsg struct {
		alias string
		err   error
	}
	tabOpenedMsg struct {
		alias string
		err   error
	}
	toolExitMsg struct {
		name string
		err  error
	}
	editorExitMsg  struct{ err error }
	toastExpireMsg struct{ id int }
)

// New builds the app and reads the ssh config.
func New(o Options) *App {
	a := &App{
		cfg:       o.Config,
		cfgPath:   o.ConfigPath,
		runner:    o.Runner,
		hist:      o.History,
		cmdFile:   o.CmdFile,
		st:        newStyles(true),
		hosts:     newHostsTab(),
		detail:    detailState{show: true},
		resolved:  map[string]*resolve.Resolved{},
		backupDir: o.BackupDir,
		checks:    newChecks(),
		sem:       make(chan struct{}, resolveWorkers),
	}
	if a.backupDir == "" {
		a.backupDir = backupDir()
	}
	a.keyDir = o.KeyDir
	if a.keyDir == "" {
		a.keyDir = keys.Dir()
	}
	if a.hist == nil {
		a.hist = history.Load("")
	}
	a.spin = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	a.spin.Style = a.st.logo
	a.hosts.filter.SetValue(o.Filter)
	a.loadSSH()
	return a
}

// Outcome is what main should do after the program exits.
func (a *App) Outcome() Outcome { return a.outcome }

func (a *App) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, a.resolveAll(), a.watch(), a.loadTailscale(), a.checkTick(), a.loadKeys())
}

// loadSSH (re)reads the ssh config and rebuilds the host list. Resolved
// values are kept until fresh ones arrive, so the screen doesn't flicker.
func (a *App) loadSSH() {
	c, err := sshconfig.Load(a.cfg.SSHConfigPath())
	a.sshErr = err
	if err != nil {
		c = &sshconfig.Config{Path: a.cfg.SSHConfigPath()}
	}
	a.ssh = c
	a.mtimes = a.fileTimes()
	a.hosts.rebuild(c)
	a.refresh()
}

// fileTimes records the modification time of the main config and every
// file it includes (a missing main file records the zero time).
func (a *App) fileTimes() map[string]time.Time {
	m := map[string]time.Time{a.ssh.Path: {}}
	for _, f := range a.ssh.Files {
		m[f.Path] = time.Time{}
	}
	for p := range m {
		if fi, err := os.Stat(p); err == nil {
			m[p] = fi.ModTime()
		}
	}
	return m
}

func (a *App) changedOnDisk() bool {
	for p, t := range a.mtimes {
		var now time.Time
		if fi, err := os.Stat(p); err == nil {
			now = fi.ModTime()
		}
		if !now.Equal(t) {
			return true
		}
	}
	return false
}

func (a *App) watch() tea.Cmd {
	return tea.Tick(watchInterval, func(time.Time) tea.Msg { return watchMsg{} })
}

func (a *App) sshOptions() resolve.Options {
	o := resolve.Options{Timeout: 10 * time.Second}
	if a.cfg.CustomSSHConfig() {
		o.ConfigFile = a.cfg.SSHConfigPath()
	}
	return o
}

// resolveAll runs `ssh -G` for every host, a few at a time.
func (a *App) resolveAll() tea.Cmd {
	a.resGen++
	gen, opts, r, sem := a.resGen, a.sshOptions(), a.runner, a.sem
	var cmds []tea.Cmd
	for _, h := range a.ssh.Hosts {
		alias := h.Alias
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			return resolvedMsg{gen: gen, alias: alias, res: resolve.One(context.Background(), r, opts, alias)}
		})
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(append(cmds, a.startSpinner())...)
}

// res is alias's resolved config, or nil while it loads.
func (a *App) res(alias string) *resolve.Resolved { return a.resolved[config.HostKey(alias)] }

func (a *App) resolving() bool {
	for _, h := range a.ssh.Hosts {
		if a.res(h.Alias) == nil {
			return true
		}
	}
	return false
}

func (a *App) busy() bool {
	return a.resolving() || a.form != nil && a.form.saving || len(a.checks.probing) > 0
}

func (a *App) startSpinner() tea.Cmd {
	if a.spinning {
		return nil
	}
	a.spinning = true
	return a.spin.Tick
}

func (a *App) notify(kind int, format string, args ...any) tea.Cmd {
	a.toast.id++
	a.toast.text = fmt.Sprintf(format, args...)
	a.toast.kind = kind
	id := a.toast.id
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return toastExpireMsg{id} })
}

// refresh rebuilds the visible host list from the current state.
func (a *App) refresh() { a.hosts.refresh(a.cfg, a.hist, a.state) }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		return a, nil

	case tea.BackgroundColorMsg:
		a.st = newStyles(msg.IsDark())
		a.spin.Style = a.st.logo
		return a, nil

	case spinner.TickMsg:
		if !a.busy() {
			a.spinning = false
			return a, nil
		}
		var cmd tea.Cmd
		a.spin, cmd = a.spin.Update(msg)
		return a, cmd

	case savedMsg:
		return a, a.handleSaved(msg)

	case resolvedMsg:
		if msg.gen != a.resGen {
			return a, nil
		}
		a.resolved[config.HostKey(msg.alias)] = msg.res
		if !a.resolving() {
			return a, a.checkAll() // every host has an address now
		}
		return a, nil

	case reachMsg:
		if msg.gen == a.checks.gen {
			a.checks.reach[config.HostKey(msg.alias)] = msg.reach
			if a.hosts.sort == sortStatus {
				a.refresh()
			}
		}
		return a, nil

	case tailscaleMsg:
		a.checks.peers, a.checks.tsErr = msg.peers, msg.err
		a.refresh()
		return a, nil

	case checkTickMsg:
		return a, tea.Batch(a.recheck(), a.checkTick())

	case loginMsg:
		return a, a.handleLogin(msg)

	case trustMsg:
		return a, a.handleTrust(msg)

	case keysMsg:
		a.handleKeys(msg)
		return a, nil

	case focusKeyMsg:
		a.keys.focus(msg.name)
		return a, nil

	case keyGenMsg:
		return a, a.handleKeyGen(msg)

	case agentAddMsg:
		return a, a.handleAgentAdd(msg)

	case copyDoneMsg:
		return a, a.handleCopyDone(msg)

	case watchMsg:
		if a.changedOnDisk() {
			a.loadSSH()
			return a, tea.Batch(a.watch(), a.resolveAll(), a.notify(0, "%s changed: reloaded", config.TildePath(a.ssh.Path)))
		}
		return a, a.watch()

	case sessionEndMsg:
		a.refresh()
		if msg.err != nil {
			return a, a.notify(2, "ssh %s: %v", msg.alias, msg.err)
		}
		return a, nil

	case tabOpenedMsg:
		if msg.err != nil {
			return a, a.notify(2, "Opening %s: %v", msg.alias, msg.err)
		}
		return a, nil

	case toolExitMsg:
		if msg.err != nil {
			return a, a.notify(2, "%s exited: %v", msg.name, msg.err)
		}
		return a, nil

	case editorExitMsg:
		a.loadSSH()
		cmds := []tea.Cmd{a.resolveAll()}
		if msg.err != nil {
			cmds = append(cmds, a.notify(2, "Editor: %v", msg.err))
		}
		return a, tea.Batch(cmds...)

	case toastExpireMsg:
		if msg.id == a.toast.id {
			a.toast.text = ""
		}
		return a, nil

	case tea.KeyPressMsg:
		return a, a.handleKey(msg)
	}

	// Let a focused input receive anything else (pastes, cursor blink).
	if a.form != nil {
		return a, a.updateForm(msg)
	}
	if a.keyForm != nil {
		return a, a.updateKeyForm(msg)
	}
	return a, a.updateFilter(msg)
}

func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	if a.help {
		a.helpKey(key)
		return nil
	}
	if a.form != nil {
		return a.formKey(msg)
	}
	if a.keyForm != nil {
		return a.keyFormKey(msg)
	}
	if a.popup != nil {
		return a.popupKey(key)
	}
	if f := a.activeFilter(); f != nil && f.Focused() {
		switch key {
		case "esc":
			f.SetValue("")
			f.Blur()
		case "enter":
			f.Blur()
			a.refresh()
			// Enter in the filter connects when it narrowed things to one host.
			if len(a.hosts.hostRows()) == 1 {
				return a.connectSelected()
			}
			return nil
		case "up", "down":
			f.Blur()
		default:
			cmd := a.updateFilter(msg)
			a.refresh()
			return cmd
		}
		a.refresh()
		return nil
	}

	switch key {
	case "q":
		return tea.Quit
	case "?":
		a.help, a.helpScroll = true, 0
		return nil
	case "tab":
		return a.setTab((a.tab + 1) % len(tabNames))
	case "shift+tab":
		return a.setTab((a.tab + len(tabNames) - 1) % len(tabNames))
	case "1", "2", "3", "4":
		return a.setTab(int(key[0] - '1'))
	case "/":
		if f := a.activeFilter(); f != nil {
			return f.Focus()
		}
	case "esc":
		if a.copyKey != nil {
			a.copyKey = nil
			return a.notify(0, "Not copying the key")
		}
		if f := a.activeFilter(); f != nil && f.Value() != "" {
			f.SetValue("")
			a.refresh()
			return nil
		}
		if a.detailLayout() == layoutFull {
			a.detail.full = false
			return nil
		}
		if a.hosts.clearSelection() {
			return nil
		}
	}

	switch a.tab {
	case tabHosts:
		return a.hostsKey(key)
	case tabKeys:
		return a.keysKey(key)
	case tabSettings:
		return a.settingsKey(key)
	}
	return nil
}

// activeFilter is the filter input of the current tab, if it has one.
func (a *App) activeFilter() *textinput.Model {
	if a.tab == tabHosts {
		return &a.hosts.filter
	}
	return nil
}

func (a *App) updateFilter(msg tea.Msg) tea.Cmd {
	f := a.activeFilter()
	if f == nil || !f.Focused() {
		return nil
	}
	var cmd tea.Cmd
	*f, cmd = f.Update(msg)
	return cmd
}

// setTab switches tabs. Opening Settings re-checks for tools installed
// while quick-ssh was running.
func (a *App) setTab(tab int) tea.Cmd {
	a.tab = tab
	if tab == tabKeys {
		return a.loadKeys() // keys made or loaded outside quick-ssh
	}
	if tab != tabSettings {
		return nil
	}
	names := a.cfg.EnableInstalled()
	if len(names) == 0 {
		return nil
	}
	return tea.Batch(a.saveConfig(), a.notify(1, "Found and enabled %s", strings.Join(names, ", ")))
}

// enabledTools returns the tools switched on in settings.
func (a *App) enabledTools() []config.Tool {
	var ts []config.Tool
	for _, t := range a.cfg.Tools {
		if t.Enabled {
			ts = append(ts, t)
		}
	}
	return ts
}

func (a *App) saveConfig() tea.Cmd {
	if err := a.cfg.Save(a.cfgPath); err != nil {
		return a.notify(2, "Saving config: %v", err)
	}
	return nil
}

// ---- View ----

func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.WindowTitle = "quick-ssh"
	return v
}

func (a *App) render() string {
	if a.w == 0 || a.h == 0 {
		return ""
	}
	header := a.renderHeader()
	rule := a.st.rule.Render(strings.Repeat("─", a.w))

	bodyH := a.h - chromeLines
	var body string
	switch a.tab {
	case tabHosts:
		body = a.renderHostsBody(bodyH)
	case tabDiscover:
		body = a.renderComingSoon("Discover",
			"Tailnet devices and known_hosts entries that aren't in your ssh config yet,",
			"ready to add with a key press. Coming in milestone M5.")
	case tabKeys:
		body = a.renderKeys(bodyH)
	case tabSettings:
		body = a.renderSettings(bodyH)
	}
	body = padLines(body, bodyH, a.w)

	base := strings.Join([]string{header, rule, body, a.renderStatusBar()}, "\n")

	var overlay string
	switch {
	case a.help:
		overlay = a.renderHelp()
	case a.form != nil:
		overlay = a.renderForm()
	case a.keyForm != nil:
		overlay = a.renderKeyForm()
	case a.popup != nil:
		overlay = a.renderPopup()
	}
	if overlay == "" {
		return base
	}
	// Popups are sized to fit, but on a tiny terminal they're cut to the
	// screen rather than pushing it out of shape.
	if ls := strings.Split(overlay, "\n"); len(ls) > a.h || lipgloss.Width(overlay) > a.w {
		ls = ls[:min(len(ls), a.h)]
		for i, l := range ls {
			ls[i] = ansi.Truncate(l, a.w, "")
		}
		overlay = strings.Join(ls, "\n")
	}
	ow, oh := lipgloss.Width(overlay), lipgloss.Height(overlay)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(overlay).X(max(0, (a.w-ow)/2)).Y(max(0, (a.h-oh)/2)).Z(1),
	).Render()
}

func (a *App) renderComingSoon(title string, lines ...string) string {
	out := []string{"", "  " + a.st.boxTitle.Render(title), ""}
	for _, l := range lines {
		out = append(out, "  "+a.st.dim.Render(l))
	}
	return strings.Join(out, "\n")
}

func padLines(s string, h, w int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = fit(l, w)
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderHeader() string {
	logo := a.st.logo.Render(" ◆ quick-ssh ")
	counts := []string{fmt.Sprintf("%d", len(a.ssh.Hosts)-a.hosts.hiddenCount()), "", "", ""}
	if a.keys.loaded {
		counts[tabKeys] = fmt.Sprintf("%d", len(a.keys.list))
	}
	var tabs []string
	for i, name := range tabNames {
		label := name
		if counts[i] != "" {
			label += " " + counts[i]
		}
		if i == a.tab {
			tabs = append(tabs, a.st.tabOn.Render(label))
		} else {
			tabs = append(tabs, a.st.tabOff.Render(label))
		}
	}
	left := logo + " " + strings.Join(tabs, " ")

	right := a.st.dim.Render(config.TildePath(a.ssh.Path)) + " "
	gap := a.w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fit(left, a.w)
	}
	return left + strings.Repeat(" ", gap) + right
}

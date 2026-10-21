package ui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/probe"
	"github.com/bferg314/quick-ssh/internal/sshconfig"
	"github.com/bferg314/quick-ssh/internal/status"
	"github.com/bferg314/quick-ssh/internal/tailscale"
)

// dialWorkers is how many reachability checks run at once.
const dialWorkers = 16

type (
	reachMsg struct {
		gen   int
		alias string
		reach probe.Reach
	}
	tailscaleMsg struct {
		peers []*tailscale.Peer
		err   error
	}
	checkTickMsg struct{}
	loginMsg     struct {
		alias string
		login probe.Login
		// For a single check that found a host key problem, the key the
		// host presents now (or why it couldn't be fetched).
		hostKey *probe.HostKey
		keyErr  error
		single  bool
	}
	trustMsg struct {
		alias string
		err   error
	}
)

// checks holds everything learned about the hosts' status.
type checks struct {
	gen     int
	reach   map[string]probe.Reach // by host key
	logins  map[string]probe.Login
	probing map[string]bool
	peers   []*tailscale.Peer
	tsErr   error
	sem     chan struct{}
}

func newChecks() checks {
	return checks{reach: map[string]probe.Reach{}, logins: map[string]probe.Login{},
		probing: map[string]bool{}, sem: make(chan struct{}, dialWorkers)}
}

// checkAll dials every host that's reached directly. Results replace the
// old ones as they arrive, so the column doesn't blank out while checking.
func (a *App) checkAll() tea.Cmd {
	a.checks.gen++
	gen, sem, timeout := a.checks.gen, a.checks.sem, a.cfg.Timeout()
	var cmds []tea.Cmd
	for _, h := range a.ssh.Hosts {
		res := a.res(h.Alias)
		if res == nil || res.Err != nil || res.Jump() != "" || res.HostName == "" {
			continue
		}
		alias, host, port := h.Alias, res.HostName, res.Port
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			return reachMsg{gen: gen, alias: alias, reach: probe.Dial(context.Background(), host, port, timeout)}
		})
	}
	return tea.Batch(cmds...)
}

func (a *App) loadTailscale() tea.Cmd {
	if !a.cfg.Tailscale || !config.Available("tailscale") {
		a.checks.peers, a.checks.tsErr = nil, nil
		return nil
	}
	r := a.runner
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		peers, err := tailscale.Status(ctx, r)
		return tailscaleMsg{peers: peers, err: err}
	}
}

// checkTick re-checks every host at the configured interval.
func (a *App) checkTick() tea.Cmd {
	d := a.cfg.Interval()
	if d <= 0 {
		return nil
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return checkTickMsg{} })
}

// recheck reloads Tailscale's view and dials every host again.
func (a *App) recheck() tea.Cmd { return tea.Batch(a.loadTailscale(), a.checkAll()) }

// peer is the tailnet machine a host points at, if any.
func (a *App) peer(h *sshconfig.Host) *tailscale.Peer {
	res := a.res(h.Alias)
	if res == nil || res.Err != nil {
		return nil
	}
	return tailscale.Match(a.checks.peers, res.HostName)
}

// state is a host's status. depth guards against ProxyJump loops.
func (a *App) state(h *sshconfig.Host) status.State { return a.stateAt(h, 0) }

func (a *App) stateAt(h *sshconfig.Host, depth int) status.State {
	k := config.HostKey(h.Alias)
	in := status.Input{Res: a.res(h.Alias), Peer: a.peer(h)}
	if r, ok := a.checks.reach[k]; ok {
		in.Reach = &r
	}
	if l, ok := a.checks.logins[k]; ok {
		in.Login = &l
	}
	if in.Res != nil && in.Res.Err == nil && in.Res.ProxyJump != "" && depth < 5 {
		if j := a.ssh.Find(status.FirstHop(in.Res.ProxyJump)); j != nil {
			s := a.stateAt(j, depth+1)
			in.Jump = &s
		}
	}
	return status.Of(in)
}

func (a *App) stateColor(k status.Kind) color.Color {
	return map[status.Kind]color.Color{
		status.Up: a.st.green, status.Checking: a.st.muted, status.Unknown: a.st.muted,
		status.Warn: a.st.yellow, status.Down: a.st.red, status.Offline: a.st.muted,
	}[k]
}

// statusCell is the status column: a dot and a few words.
func (a *App) statusCell(h *sshconfig.Host) string {
	st := a.st
	s := a.state(h)
	if a.checks.probing[config.HostKey(h.Alias)] {
		return a.spin.View() + st.dim.Render(" login")
	}
	dot := map[status.Kind]string{
		status.Up: "●", status.Checking: "◌", status.Unknown: "◌", status.Warn: "▲", status.Down: "○", status.Offline: "○",
	}[s.Kind]
	style := st.textS.Foreground(a.stateColor(s.Kind))
	text := s.Text
	if s.Kind == status.Checking {
		return st.faintText.Render(dot)
	}
	if s.Kind == status.Up {
		return style.Render(dot) + " " + st.dim.Render(text)
	}
	return style.Render(dot + " " + text)
}

// counts are how many visible hosts are up and down, for the status bar.
func (a *App) counts() (up, down int) {
	for _, r := range a.hosts.hostRows() {
		switch a.state(r.host).Kind {
		case status.Up:
			up++
		case status.Down, status.Offline:
			down++
		}
	}
	return up, down
}

// ---- login checks ----

// checkLogins tries key authentication on the selected hosts, or the one
// under the cursor (t). A single check that finds a host key problem
// goes on to show the key, ready to trust.
func (a *App) checkLogins() tea.Cmd {
	aliases := a.hosts.selectedAliases()
	if len(aliases) == 0 {
		if h := a.hosts.currentHost(); h != nil {
			aliases = []string{h.Alias}
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	single := len(aliases) == 1
	r, opts := a.runner, a.sshOptions()
	cmds := []tea.Cmd{a.startSpinner()}
	for _, alias := range aliases {
		a.checks.probing[config.HostKey(alias)] = true
		res := a.res(alias)
		cmds = append(cmds, func() tea.Msg {
			ctx := context.Background()
			m := loginMsg{alias: alias, single: single, login: probe.CheckLogin(ctx, r, opts, alias)}
			if single && res != nil && res.Err == nil &&
				(m.login.Outcome == probe.HostKeyChanged || m.login.Outcome == probe.HostKeyUnknown) {
				m.hostKey, m.keyErr = probe.ScanHostKey(ctx, r, opts, alias, res)
			}
			return m
		})
	}
	if !single {
		cmds = append(cmds, a.notify(0, "Checking logins on %d hosts…", len(aliases)))
	}
	return tea.Batch(cmds...)
}

func (a *App) handleLogin(msg loginMsg) tea.Cmd {
	k := config.HostKey(msg.alias)
	delete(a.checks.probing, k)
	a.checks.logins[k] = msg.login
	a.refresh()

	if !msg.single {
		if len(a.checks.probing) > 0 {
			return nil
		}
		return a.loginSummary()
	}
	l := msg.login
	switch {
	case msg.hostKey != nil:
		a.popup = a.hostKeyPopup(msg.hostKey, l.Outcome)
		return nil
	case msg.keyErr != nil:
		return a.notify(2, "%s: %s; %v", msg.alias, l.Outcome, msg.keyErr)
	case l.Outcome == probe.OK:
		return a.notify(1, "%s: login works", msg.alias)
	case l.Detail != "":
		return a.notify(2, "%s: %s (%s)", msg.alias, l.Outcome, l.Detail)
	}
	return a.notify(2, "%s: %s", msg.alias, l.Outcome)
}

// loginSummary reports a batch of login checks once they've all finished.
func (a *App) loginSummary() tea.Cmd {
	ok, bad := 0, []string{}
	for _, r := range a.hosts.hostRows() {
		l, done := a.checks.logins[config.HostKey(r.host.Alias)]
		if !done || time.Since(l.At) > time.Minute {
			continue
		}
		if l.Outcome == probe.OK {
			ok++
		} else {
			bad = append(bad, r.host.Alias)
		}
	}
	if len(bad) == 0 {
		return a.notify(1, "Login works on all %d", ok)
	}
	return a.notify(2, "Login works on %d; not on %s (see details)", ok, strings.Join(bad, ", "))
}

// hostKeyPopup shows the key a host presents next to what known_hosts
// has, and offers to trust it.
func (a *App) hostKeyPopup(hk *probe.HostKey, o probe.Outcome) *actionPopup {
	p := &actionPopup{title: hk.Alias + "'s host key changed"}
	if o == probe.HostKeyUnknown {
		p.title = hk.Alias + "'s host key isn't known yet"
	}
	join := func(fps []string) string {
		if len(fps) == 0 {
			return "nothing"
		}
		return strings.Join(fps, ", ")
	}
	p.body = []string{
		"known_hosts has  " + join(hk.Old),
		hk.Alias + " presents  " + join(hk.New),
	}
	if o == probe.HostKeyChanged {
		p.warning = "If " + hk.Alias + " wasn't reinstalled or given new keys, someone may be in the middle. Check before trusting."
	}
	p.actions = []popupAction{
		{key: "y", label: "Trust this key", note: "in " + config.TildePath(hk.KnownHosts) + "; ssh-keygen keeps known_hosts.old",
			run: func() tea.Cmd { return a.trust(hk) }},
		{key: "esc", label: "Cancel", run: func() tea.Cmd { return nil }},
	}
	return p
}

func (a *App) trust(hk *probe.HostKey) tea.Cmd {
	r, opts := a.runner, a.sshOptions()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return trustMsg{alias: hk.Alias, err: probe.Trust(ctx, r, opts, hk)}
	}
}

func (a *App) handleTrust(msg trustMsg) tea.Cmd {
	if msg.err != nil {
		return a.notify(2, "Trusting %s's key: %v", msg.alias, msg.err)
	}
	delete(a.checks.logins, config.HostKey(msg.alias))
	a.hosts.focus(msg.alias)
	return tea.Batch(a.notify(1, "Trusted %s's host key", msg.alias), a.checkLogins())
}

// ---- detail pane ----

func (a *App) appendStatus(h *sshconfig.Host, add func(string), section func(string)) {
	st := a.st
	k := config.HostKey(h.Alias)
	label := func(s string) string { return st.dim.Render(fmt.Sprintf("%-10s", s)) + " " }
	section("STATUS")

	s := a.state(h)
	reach := a.statusCell(h)
	if r, ok := a.checks.reach[k]; ok && s.Kind != status.Offline && !strings.HasPrefix(s.Text, "via") {
		reach += st.faintText.Render("  checked " + ago(r.At))
	}
	add(label("reachable") + reach)

	if p := a.peer(h); p != nil {
		line := st.ok.Render("online")
		if !p.Online {
			line = st.dim.Render("offline · last seen " + ago(p.LastSeen))
		}
		if p.OS != "" {
			line += st.dim.Render(" · " + p.OS)
		}
		add(label("tailscale") + line)
	}

	switch l, ok := a.checks.logins[k]; {
	case a.checks.probing[k]:
		add(label("login") + a.spin.View() + st.dim.Render(" checking…"))
	case !ok:
		add(label("login") + st.faintText.Render("not checked · t checks"))
	case l.Outcome == probe.OK:
		add(label("login") + st.ok.Render("✓ works") + st.faintText.Render("  "+ago(l.At)))
	default:
		style := st.bad
		if l.Outcome == probe.NeedsAuth {
			style = st.warn
		}
		add(label("login") + style.Render("✗ "+l.Outcome.String()) + st.faintText.Render("  "+ago(l.At)))
		if l.Detail != "" {
			add(label("") + st.dim.Render(l.Detail))
		}
		if l.Outcome == probe.HostKeyChanged || l.Outcome == probe.HostKeyUnknown {
			add(label("") + st.faintText.Render("t shows the new key and offers to trust it"))
		}
	}
}

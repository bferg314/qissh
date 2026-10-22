package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/history"
	"github.com/bferg314/qissh/internal/resolve"
)

const testSSHConfig = `# test hosts
Host elm
    HostName elm.lan
    User root

Host oak oak2
    HostName oak.example.ts.net
    User alice
    IdentityFile ~/.ssh/nope_key

Host devbox1
    HostName devbox1.example.ts.net
    ProxyJump elm

Host *
    ServerAliveInterval 30
`

// fakeRunner plays ssh (-G, login checks, host key scans), ssh-keygen and
// tailscale.
type fakeRunner struct {
	// logins is what a login check on each alias prints; a missing alias
	// logs in fine.
	logins map[string]string
	// knownHosts is reported as every host's UserKnownHostsFile.
	knownHosts string
	tailscale  string // `tailscale status --json`
	// agent is what `ssh-add -l` prints; "" means no agent.
	agent string
	// keyDir holds the test keys; elm names its id_ed25519.
	keyDir string
	calls  *[]string
}

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if f.calls != nil {
		*f.calls = append(*f.calls, name+" "+strings.Join(args, " "))
	}
	switch {
	case name == "tailscale":
		if f.tailscale == "" {
			return nil, errors.New("not running")
		}
		return []byte(f.tailscale), nil
	case name == "ssh-keygen" && args[0] == "-l" && args[1] == "-f":
		return []byte("256 SHA256:NEWKEY oak (ED25519)\n"), nil
	case name == "ssh-keygen" && args[0] == "-l" && args[1] == "-F":
		return []byte("oak.example.ts.net ED25519 SHA256:OLDKEY\n"), nil
	case name == "ssh-keygen":
		return nil, nil
	}
	if len(args) == 4 && args[0] == "-F" && args[2] == "-G" {
		// Validating a config before saving it.
		if data, _ := os.ReadFile(args[1]); strings.Contains(string(data), "BADOPT") {
			return nil, errors.New(args[1] + ": line 3: Bad configuration option: badopt")
		}
		return []byte("hostname x\n"), nil
	}
	alias := args[len(args)-1]
	out := map[string]string{
		"elm":     "hostname elm.lan\nuser root\nport 22\n",
		"oak":     "hostname oak.example.ts.net\nuser alice\nport 22\nidentityfile ~/.ssh/nope_key\n",
		"oak2":    "hostname oak.example.ts.net\nuser alice\nport 22\n",
		"devbox1": "hostname devbox1.example.ts.net\nuser me\nport 2222\nproxyjump elm\n",
	}[alias]
	if alias == "elm" && f.keyDir != "" {
		out += "identityfile " + filepath.ToSlash(filepath.Join(f.keyDir, "id_ed25519")) + "\n"
	}
	if f.knownHosts != "" {
		out += "userknownhostsfile " + f.knownHosts + "\n"
	}
	return []byte(out), nil
}

// Combined is ssh connecting: a login check, or a host key scan into a
// throwaway known_hosts.
func (f fakeRunner) Combined(_ context.Context, name string, args ...string) ([]byte, error) {
	if f.calls != nil {
		*f.calls = append(*f.calls, name+" "+strings.Join(args, " "))
	}
	if strings.HasSuffix(name, "ssh-add") {
		if f.agent == "" {
			return []byte("Could not open a connection to your authentication agent.\n"), errors.New("exit status 2")
		}
		return []byte(f.agent), nil
	}
	for i, a := range args {
		if a == "-o" && strings.HasPrefix(args[i+1], "UserKnownHostsFile=") {
			os.WriteFile(strings.TrimPrefix(args[i+1], "UserKnownHostsFile="), []byte("oak.example.ts.net ssh-ed25519 AAAANEW\n"), 0o600)
			return nil, nil
		}
	}
	alias, command := args[len(args)-2], args[len(args)-1] // ... alias "exit 0"
	if out, ok := f.logins[alias]; ok {
		return []byte(out), errors.New("exit status 255")
	}
	return remoteCommand(alias, command)
}

type testEnv struct {
	a       *App
	sshPath string
	hist    *history.History
	fake    fakeRunner
	calls   *[]string
	tunnels *fakeTunnels
}

func newTestApp(t *testing.T, w, h int) *testEnv {
	t.Helper()
	t.Setenv("ZELLIJ", "")
	t.Setenv("WT_SESSION", "")
	t.Setenv("VISUAL", "qissh-test-no-editor")
	old := writeClipboard
	writeClipboard = func(string) error { return nil }
	t.Cleanup(func() { writeClipboard = old })
	// The same on every machine: Tailscale installed and nothing else, and
	// a local user no test host uses.
	oldAvailable, oldUser := available, localUser
	available = func(cmd string) bool { return cmd == "tailscale" }
	localUser = func() string { return "tester" }
	t.Cleanup(func() { available, localUser = oldAvailable, oldUser })

	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(sshPath, []byte(testSSHConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SSHConfig = sshPath
	cfg.ConnectMode = config.ConnectInPlace
	hist := history.Load(filepath.Join(dir, "history.json"))
	calls := &[]string{}
	keyDir := filepath.Join(dir, "keys")
	agentKey := writeTestKeys(t, keyDir)
	tunnels := &fakeTunnels{}
	fake := fakeRunner{logins: map[string]string{}, knownHosts: filepath.ToSlash(filepath.Join(dir, "known_hosts")), calls: calls,
		agent: "256 " + agentKey + " me@here (ED25519)\n", keyDir: keyDir}
	a := New(Options{Config: cfg, ConfigPath: filepath.Join(dir, "config.toml"), Runner: fake, History: hist,
		BackupDir: filepath.Join(dir, "backups"), KeyDir: keyDir, FactsPath: filepath.Join(dir, "facts.json"), StartTunnel: tunnels.start})
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	resolveSync(a)
	a.Update(a.loadKeys()())
	return &testEnv{a: a, sshPath: sshPath, hist: hist, fake: fake, calls: calls, tunnels: tunnels}
}

// resolveSync does what resolveAll does, without the event loop.
func resolveSync(a *App) {
	for _, h := range a.ssh.Hosts {
		a.Update(resolvedMsg{gen: a.resGen, alias: h.Alias, res: resolve.One(context.Background(), a.runner, a.sshOptions(), h.Alias)})
	}
}

func press(a *App, keys ...string) {
	for _, k := range keys {
		switch k {
		case "enter":
			a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "esc":
			a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		case "space":
			a.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		case "tab":
			a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		case "shift+tab":
			a.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		case "ctrl+s":
			a.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		case "home":
			a.Update(tea.KeyPressMsg{Code: tea.KeyHome})
		case "end":
			a.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
		default:
			for _, r := range k {
				a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		}
	}
}

func screen(a *App) string { return ansi.Strip(a.render()) }

func bar(a *App) string {
	lines := strings.Split(screen(a), "\n")
	return lines[len(lines)-1]
}

func cursorAlias(a *App) string {
	if h := a.hosts.currentHost(); h != nil {
		return h.Alias
	}
	return ""
}

func TestTabsSwitch(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	for i, mode := range []string{"HOSTS", "DISCOVER", "KEYS", "SETTINGS"} {
		press(a, string(rune('1'+i)))
		if a.tab != i || !strings.HasPrefix(strings.TrimSpace(bar(a)), mode) {
			t.Fatalf("key %d: tab=%d bar=%q", i+1, a.tab, bar(a))
		}
	}
	press(a, "tab")
	if a.tab != tabHosts {
		t.Fatalf("tab should wrap to Hosts, got %d", a.tab)
	}
}

func TestHostsAndRulesListed(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	s := screen(a)
	for _, want := range []string{"elm", "root@elm.lan", "oak2", "me@devbox1.example.ts.net:2222 via elm", "DEFAULTS & RULES", "Host *", "never"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "Hosts 4") {
		t.Errorf("tab count:\n%s", s)
	}
}

func TestEnterConnectsInPlace(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	press(a, "/", "devb", "enter") // the filter narrows to one host, so enter connects
	o := a.Outcome()
	if strings.Join(o.Argv, " ") != "ssh -F "+e.sshPath+" devbox1" || o.ViaShell {
		t.Fatalf("outcome %+v", o)
	}
	if h := e.hist.Get("devbox1"); h == nil || h.Count != 1 {
		t.Fatalf("connection not recorded: %+v", h)
	}
}

func TestShellModeNeedsWrapper(t *testing.T) {
	e := newTestApp(t, 120, 30)
	a := e.a
	a.cfg.ConnectMode = config.ConnectShell
	press(a, "enter")
	if o := a.Outcome(); len(o.Argv) == 0 || o.ViaShell {
		t.Fatalf("without the wrapper ssh runs in place: %+v", o)
	}
	a.cmdFile = filepath.Join(t.TempDir(), "cmd")
	press(a, "enter")
	if o := a.Outcome(); !o.ViaShell {
		t.Fatalf("with the wrapper the shell runs it: %+v", o)
	}
}

func TestMultiSelectNeedsTabs(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "space", "space")
	if got := a.hosts.selectedAliases(); len(got) != 2 {
		t.Fatalf("selected %v", got)
	}
	press(a, "enter")
	if len(a.Outcome().Argv) != 0 || !strings.Contains(a.toast.text, "zellij or Windows Terminal") {
		t.Fatalf("several hosts in place should be refused: %+v %q", a.Outcome(), a.toast.text)
	}
	press(a, "esc")
	if len(a.hosts.selected) != 0 {
		t.Fatal("esc clears the selection")
	}
}

func TestSortMostUsedFirst(t *testing.T) {
	e := newTestApp(t, 120, 30)
	// With no history hosts sort by name, so the cursor starts on devbox1.
	e.hist.RecordAt("oak2", time.Now())
	e.a.refresh()
	if cursorAlias(e.a) != "devbox1" {
		t.Fatalf("the cursor stays on the same host when the order changes: %s", cursorAlias(e.a))
	}
	if first := e.a.hosts.view[0].host.Alias; first != "oak2" {
		t.Fatalf("most used first, got %s", first)
	}
	press(e.a, "s") // name
	if first := e.a.hosts.view[0].host.Alias; first != "devbox1" {
		t.Fatalf("by name, got %s", first)
	}
	press(e.a, "s") // file order
	if first := e.a.hosts.view[0].host.Alias; first != "elm" {
		t.Fatalf("file order, got %s", first)
	}
}

func TestPinAndHide(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "s", "s", "home") // file order: elm, oak, oak2, devbox1
	press(a, "j", "*")
	if a.hosts.view[0].host.Alias != "oak" || !strings.Contains(screen(a), "★") {
		t.Fatalf("pinned host goes first:\n%s", screen(a))
	}
	press(a, "h")
	if len(a.hosts.hostRows()) != 3 || a.hosts.hiddenCount() != 1 {
		t.Fatalf("hidden host is left out:\n%s", screen(a))
	}
	press(a, "H")
	if !strings.Contains(screen(a), "⊘") {
		t.Fatalf("H shows hidden hosts:\n%s", screen(a))
	}
}

func TestDetailPane(t *testing.T) {
	a := newTestApp(t, 130, 40).a
	press(a, "s", "s", "home", "j") // oak
	s := screen(a)
	for _, want := range []string{"CONNECTION", "~/.ssh/nope_key (not found)", "shares its block with oak2", "BLOCK", "IdentityFile ~/.ssh/nope_key", "never connected"} {
		if !strings.Contains(s, want) {
			t.Errorf("detail lacks %q:\n%s", want, s)
		}
	}
	press(a, "j", "j") // devbox1: port and user come from elsewhere
	if s := screen(a); !strings.Contains(s, "2222  default") || !strings.Contains(s, "me  default") || !strings.Contains(s, "jump       elm") {
		t.Errorf("inherited values are marked:\n%s", s)
	}
}

func TestReloadWhenFileChanges(t *testing.T) {
	e := newTestApp(t, 120, 30)
	later := time.Now().Add(time.Minute)
	if err := os.WriteFile(e.sshPath, []byte(testSSHConfig+"\nHost newbox\n  HostName n.lan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(e.sshPath, later, later)
	e.a.Update(watchMsg{})
	if e.a.ssh.Find("newbox") == nil || !strings.Contains(e.a.toast.text, "reloaded") {
		t.Fatalf("not reloaded: %q", e.a.toast.text)
	}
}

func TestCopyMenu(t *testing.T) {
	var got string
	a := newTestApp(t, 120, 30).a
	writeClipboard = func(s string) error { got = s; return nil }
	press(a, "y")
	if a.popup == nil {
		t.Fatal("y opens the copy menu")
	}
	press(a, "h")
	if got != a.res(cursorAlias(a)).HostName || a.popup != nil {
		t.Fatalf("copied %q", got)
	}
}

func TestActionsPopup(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "m")
	s := screen(a)
	if a.popup == nil || !strings.Contains(s, "Connect") || !strings.Contains(s, "Edit in editor") {
		t.Fatalf("popup:\n%s", s)
	}
	press(a, "esc")
	if a.popup != nil {
		t.Fatal("esc closes it")
	}
}

func TestSettingsCycleConnectMode(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	press(a, "4", "j", "enter")
	if a.cfg.ConnectMode != config.ConnectZellij {
		t.Fatalf("mode %q", a.cfg.ConnectMode)
	}
	if _, err := os.Stat(a.cfgPath); err != nil {
		t.Fatal("the change is saved:", err)
	}
}

func TestMissingSSHConfig(t *testing.T) {
	e := newTestApp(t, 120, 30)
	os.Remove(e.sshPath)
	e.a.Update(watchMsg{})
	if s := screen(e.a); !strings.Contains(s, "No hosts in") {
		t.Fatalf("empty state:\n%s", s)
	}
}

// Every screen must fill the terminal exactly, at any size.
func TestEveryScreenFits(t *testing.T) {
	screens := map[string]func(a *App){
		"hosts":      func(*App) {},
		"filter":     func(a *App) { press(a, "/", "p") },
		"popup":      func(a *App) { press(a, "m") },
		"copy":       func(a *App) { press(a, "y") },
		"help":       func(a *App) { press(a, "?") },
		"details":    func(a *App) { press(a, "d") },
		"rule":       func(a *App) { press(a, "end") },
		"discover":   func(a *App) { press(a, "2") },
		"devices":    func(a *App) { withDevices(t, a); press(a, "2") },
		"add many":   func(a *App) { withDevices(t, a); press(a, "2", "a", "enter") },
		"add one":    func(a *App) { withDevices(t, a); press(a, "2", "enter") },
		"keys":       func(a *App) { press(a, "3") },
		"new key":    func(a *App) { press(a, "3", "n") },
		"key menu":   func(a *App) { press(a, "3", "enter") },
		"pick key":   func(a *App) { press(a, "C") },
		"confirm":    func(a *App) { press(a, "C", "1") },
		"pick hosts": func(a *App) { press(a, "3", "C") },
		"settings":   func(a *App) { press(a, "4") },
		"run":        func(a *App) { press(a, "a", "x") },
		"run output": func(a *App) {
			press(a, "a", "x", "uptime")
			_, c := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			runCmd(a, c)
		},
		"doctor":      func(a *App) { press(a, "!") },
		"tunnels":     func(a *App) { press(a, "T") },
		"new tunnel":  func(a *App) { press(a, "T", "n", "5432") },
		"facts":       func(a *App) { _, c := a.Update(tea.KeyPressMsg{Code: 'i', Text: "i"}); runCmd(a, c); press(a, "d") },
		"new host":    func(a *App) { press(a, "n") },
		"edit":        func(a *App) { press(a, "e") },
		"delete?":     func(a *App) { press(a, "e"); a.form.confirmDelete = true },
		"form error":  func(a *App) { press(a, "n", "web*"); saveKey(a) },
		"device menu": func(a *App) { withDevices(t, a); press(a, "2", "m") },
		"toast": func(a *App) {
			a.notify(2, "Can't edit: %s", strings.Repeat("a very long reason ", 12))
		},
	}
	// Screens that need the fake runner set up first.
	envScreens := map[string]func(e *testEnv){
		"host key": func(e *testEnv) {
			os.WriteFile(filepath.FromSlash(e.fake.knownHosts), []byte("oak.example.ts.net ssh-ed25519 AAAAOLD\n"), 0o600)
			e.fake.logins["oak"] = changedBanner
			e.a.hosts.focus("oak")
			_, c := e.a.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
			runCmd(e.a, c)
		},
		"login detail": func(e *testEnv) {
			e.fake.logins["oak"] = "Permission denied (publickey,password)."
			e.a.hosts.focus("oak")
			_, c := e.a.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
			runCmd(e.a, c)
			press(e.a, "d")
		},
		"tunnel running": func(e *testEnv) {
			e.a.hosts.focus("elm")
			press(e.a, "T", "n", "5432", "tab", "pg", "ctrl+s")
			press(e.a, "d")
		},
	}
	for name, setup := range screens {
		envScreens[name] = func(e *testEnv) { setup(e.a) }
	}
	sizes := [][2]int{{40, 10}, {60, 15}, {80, 24}, {99, 30}, {100, 30}, {200, 40}, {200, 50}}
	for name, setup := range envScreens {
		for _, sz := range sizes {
			e := newTestApp(t, sz[0], sz[1])
			a := e.a
			setup(e)
			lines := strings.Split(a.render(), "\n")
			if len(lines) != sz[1] {
				t.Errorf("%s at %dx%d: %d lines", name, sz[0], sz[1], len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > sz[0] {
					t.Errorf("%s at %dx%d: line %d is %d wide: %q", name, sz[0], sz[1], i, w, ansi.Strip(l))
				}
			}
		}
	}
}

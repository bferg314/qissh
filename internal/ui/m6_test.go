package ui

import (
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/tunnel"
)

type exitErr int

func (e exitErr) Error() string { return "exit status" }
func (e exitErr) ExitCode() int { return int(e) }

const testFacts = "kernel=Linux 6.1.0\narch=aarch64\nhostname=elm\nos=Debian GNU/Linux 12 (bookworm)\ncpus=4\nuptime=273600\n" +
	"load=0.10 0.20 0.30\nmem=3500000 4000000\ndisk=10485760 31457280\nupdates=7\nreboot=yes\n"

// remoteCommand plays a host running a command: the facts script, a
// command that fails on oak, or an echo.
func remoteCommand(alias, command string) ([]byte, error) {
	switch {
	case command == "exit 0":
		return nil, nil
	case strings.HasPrefix(command, "sh -c "):
		return []byte(strings.Replace(testFacts, "hostname=elm", "hostname="+alias, 1)), nil
	case command == "fails-on-oak" && alias == "oak":
		return []byte("boom\n"), exitErr(1)
	}
	return []byte("ran " + command + " on " + alias + "\n"), nil
}

// fakeTunnels records the tunnels started; each runs until killed or
// ended by the test.
type fakeTunnels struct {
	mu    sync.Mutex
	argv  [][]string
	procs []*fakeTunnel
}

type fakeTunnel struct{ done chan error }

func (p *fakeTunnel) Wait() error { return <-p.done }
func (p *fakeTunnel) Kill() error { p.done <- errors.New("killed"); return nil }

func (f *fakeTunnels) start(name string, args ...string) (tunnel.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argv = append(f.argv, append([]string{name}, args...))
	p := &fakeTunnel{done: make(chan error, 1)}
	f.procs = append(f.procs, p)
	return p, nil
}

func TestRunOnMany(t *testing.T) {
	a := newTestApp(t, 120, 40).a
	a.hosts.focus("elm")
	press(a, "space")
	a.hosts.focus("oak")
	press(a, "space", "x")
	if a.runv == nil || !strings.Contains(screen(a), "Run on elm, oak") || bar(a)[:5] != " RUN " {
		t.Fatalf("x opens the prompt:\n%s", screen(a))
	}
	press(a, "fails-on-oak")
	press(a, "ctrl+s")
	if len(a.cfg.Snippets) != 1 || !strings.Contains(readFile(t, a.cfgPath), "fails-on-oak") {
		t.Fatalf("ctrl+s saves a snippet: %v", a.cfg.Snippets)
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runCmd(a, cmd)
	s := screen(a)
	if strings.Index(s, "✗ oak") < 0 || strings.Index(s, "✗ oak") > strings.Index(s, "✓ elm") || !strings.Contains(s, "exit 1") ||
		!strings.Contains(s, "boom") || !strings.Contains(s, "ran fails-on-oak on elm") || !strings.Contains(s, "✓ 1 ok  ✗ 1 failed") {
		t.Fatalf("failures first:\n%s", s)
	}
	if !strings.Contains(a.toast.text, "failed on 1 of 2") {
		t.Fatal(a.toast.text)
	}
	if !strings.Contains(a.runText(), "== oak (exit 1)\nboom") {
		t.Fatal(a.runText())
	}

	// e goes back to the prompt with the command; ↓ picks the snippet.
	press(a, "e")
	if a.runv.command != "" || a.runv.input.Value() != "fails-on-oak" {
		t.Fatalf("e: %q", a.runv.input.Value())
	}
	a.runv.input.SetValue("")
	a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if a.runv.input.Value() != "fails-on-oak" {
		t.Fatal("the snippet fills the input")
	}
	press(a, "esc")
	if a.runv != nil {
		t.Fatal("esc closes")
	}
}

func TestFacts(t *testing.T) {
	a := newTestApp(t, 140, 60).a
	a.hosts.focus("elm")
	if !strings.Contains(screen(a), "i fetches the OS") {
		t.Fatalf("no facts yet:\n%s", screen(a))
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	runCmd(a, cmd)
	s := screen(a)
	for _, want := range []string{"FACTS", "Debian GNU/Linux 12 (bookworm)", "Linux 6.1.0 · aarch64", "3d 4h", "0.10 0.20 0.30  (4 cpus)",
		"3.3 GiB of 3.8 GiB (87%)", "10 GiB of 30 GiB (33%)", "7 pending", "reboot", "needed"} {
		if !strings.Contains(s, want) {
			t.Errorf("facts lack %q:\n%s", want, s)
		}
	}
	if !strings.Contains(readFile(t, a.facts.Path()), "Debian") {
		t.Fatal("facts are cached")
	}
}

func TestTunnels(t *testing.T) {
	e := newTestApp(t, 140, 50)
	a := e.a
	a.hosts.focus("elm")
	press(a, "T")
	if a.popup == nil || !strings.Contains(screen(a), "No saved tunnels for elm") {
		t.Fatalf("T:\n%s", screen(a))
	}
	press(a, "n")
	if a.tunnelForm == nil {
		t.Fatal("n opens the form")
	}
	press(a, "x")
	press(a, "ctrl+s")
	if a.tunnelForm == nil || !strings.Contains(a.tunnelForm.err, "isn't a port") {
		t.Fatal("bad forward")
	}
	a.tunnelForm.inputs[0].SetValue("5432")
	press(a, "tab", "pg", "ctrl+s")
	if a.tunnelForm != nil || len(e.tunnels.argv) != 1 {
		t.Fatalf("started: %v", e.tunnels.argv)
	}
	if argv := strings.Join(e.tunnels.argv[0], " "); !strings.HasSuffix(argv, "-L 5432:localhost:5432 elm") || !strings.Contains(argv, "ExitOnForwardFailure=yes") {
		t.Fatal(argv)
	}
	if m := a.cfg.Meta("elm"); len(m.Tunnels) != 1 || m.Tunnels[0] != (config.Tunnel{Name: "pg", Spec: "L 5432:localhost:5432"}) {
		t.Fatalf("saved: %+v", m.Tunnels)
	}
	s := screen(a)
	if !strings.Contains(s, "TUNNELS") || !strings.Contains(s, "● pg") || !strings.Contains(bar(a), "⇄1 tunnel") {
		t.Fatalf("running:\n%s", s)
	}

	// Editing the host keeps its tunnels.
	press(a, "e")
	saveKey(a)
	a.form = nil
	if len(a.cfg.Meta("elm").Tunnels) != 1 {
		t.Fatal("the form dropped the tunnel")
	}

	// 1 in the menu stops it; stopping says nothing went wrong.
	press(a, "T", "1")
	if a.tunnels.Running("elm", tunnel.Spec{Kind: 'L', Arg: "5432:localhost:5432"}) {
		t.Fatal("1 stops it")
	}
	if m := <-a.tunnelEnded; m.err != nil {
		t.Fatal(m.err)
	}

	// Started again, then ssh gives up: a toast says why.
	press(a, "T", "1")
	e.tunnels.procs[1].done <- errors.New("bind: Address already in use")
	a.Update(<-a.tunnelEnded)
	if !strings.Contains(a.toast.text, "Address already in use") || strings.Contains(bar(a), "⇄") {
		t.Fatalf("%q / %s", a.toast.text, bar(a))
	}
	press(a, "T", "1")
	a.StopTunnels()
	if len(a.tunnels.List()) != 0 {
		t.Fatal("StopTunnels")
	}
}

func TestDoctor(t *testing.T) {
	a := newTestApp(t, 120, 40).a
	// oak's IdentityFile ~/.ssh/nope_key doesn't exist.
	if !strings.Contains(bar(a), "to fix") {
		t.Fatalf("bar: %s", bar(a))
	}
	press(a, "!")
	s := screen(a)
	if a.doctor == nil || !strings.Contains(s, "Config doctor") || !strings.Contains(s, "oak's IdentityFile doesn't exist") ||
		!strings.Contains(s, "fix: fix the path") || !strings.Contains(s, "id_ed25519 has no passphrase") {
		t.Fatalf("doctor:\n%s", s)
	}
	press(a, "esc")
	if a.doctor != nil {
		t.Fatal("esc closes")
	}
}

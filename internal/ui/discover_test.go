package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/qissh/internal/tailscale"
)

// withDevices gives the app a tailnet and a known_hosts with machines
// that are and aren't configured: oak is (by HostName), nas and zebra
// aren't, and box.lan is only in known_hosts.
func withDevices(t *testing.T, a *App) {
	t.Helper()
	known := "oak.example.ts.net ssh-ed25519 AAAA1\nbox.lan,10.0.0.5 ssh-ed25519 AAAA2\n100.1.1.7 ssh-ed25519 AAAA3\ngithub.com ssh-ed25519 AAAA4\n"
	if err := os.WriteFile(filepath.Join(a.keyDir, "known_hosts"), []byte(known), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Update(a.loadKnownHosts()())
	a.Update(tailscaleMsg{peers: []*tailscale.Peer{
		{DNSName: "myhost.example.ts.net.", Self: true, Online: true},
		{DNSName: "oak.example.ts.net.", OS: "linux", Online: true},
		{DNSName: "nas.example.ts.net.", OS: "linux", TailscaleIPs: []string{"100.1.1.7"}, Online: true},
		{DNSName: "zebra.example.ts.net.", OS: "windows", LastSeen: time.Now().Add(-72 * time.Hour)},
	}})
}

func TestDiscoverTab(t *testing.T) {
	a := newTestApp(t, 130, 30).a
	withDevices(t, a)
	press(a, "2")
	s := screen(a)
	for _, want := range []string{"Discover 3", "3 machines not in your ssh config", "nas", "● online", "zebra", "○ 3d ago", "box.lan", "tailnet, known_hosts",
		"adds        Host nas · HostName nas.example.ts.net · User alice"} {
		if !strings.Contains(s, want) {
			t.Errorf("discover lacks %q:\n%s", want, s)
		}
	}
	for _, not := range []string{"oak", "myhost", "github"} {
		if strings.Contains(s, not) {
			t.Errorf("%s shouldn't be listed:\n%s", not, s)
		}
	}
	press(a, "/", "w", "i", "n", "enter")
	if len(a.disc.view) != 1 || a.disc.view[0].Name != "zebra" {
		t.Fatalf("filter by OS: %d", len(a.disc.view))
	}
	press(a, "esc")

	// h hides and remembers it; H shows hidden ones.
	press(a, "home", "h")
	if len(a.disc.view) != 2 || len(a.cfg.DiscoverHidden) != 1 || !strings.Contains(readFile(t, a.cfgPath), "tailnet:nas.example.ts.net") {
		t.Fatalf("hidden: %d shown, %q", len(a.disc.view), a.cfg.DiscoverHidden)
	}
	press(a, "H")
	if len(a.disc.view) != 3 || !strings.Contains(screen(a), "⊘") {
		t.Fatalf("H shows hidden:\n%s", screen(a))
	}
}

func TestArrowsSwitchTabs(t *testing.T) {
	a := newTestApp(t, 100, 30).a
	a.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if a.tab != tabDiscover {
		t.Fatal(a.tab)
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	a.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if a.tab != tabSettings {
		t.Fatal(a.tab)
	}
}

// The milestone: a tailnet device is added in three key presses.
func TestAddInThreePresses(t *testing.T) {
	e := newTestApp(t, 120, 40)
	a := e.a
	withDevices(t, a)
	press(a, "2", "enter")
	if a.form == nil || a.form.focus != focusSave || !strings.Contains(screen(a), "Add nas.example.ts.net") {
		t.Fatalf("enter opens the form on Save:\n%s", screen(a))
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runCmd(a, cmd)
	if a.form != nil {
		t.Fatalf("not saved: %s", a.form.err)
	}
	if cfg := readFile(t, e.sshPath); !strings.Contains(cfg, "Host nas\n    HostName nas.example.ts.net\n    User alice\n") {
		t.Fatalf("config:\n%s", cfg)
	}
	if a.tab != tabHosts || a.hosts.currentHost().Alias != "nas" || a.cfg.Meta("nas").Tags[0] != "tailnet" {
		t.Fatalf("lands on the new host: tab %d", a.tab)
	}
	resolveSync(a)
	press(a, "2")
	if len(a.disc.view) != 2 || a.disc.view[0].Name == "nas" {
		t.Fatalf("nas is configured now:\n%s", screen(a))
	}
}

func TestAddSeveral(t *testing.T) {
	e := newTestApp(t, 120, 40)
	a := e.a
	withDevices(t, a)
	press(a, "2", "a", "enter")
	s := screen(a)
	if a.popup == nil || !strings.Contains(s, "Add 3 hosts") || !strings.Contains(s, "box    alice@box.lan") {
		t.Fatalf("confirm:\n%s", s)
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runCmd(a, cmd)
	cfg := readFile(t, e.sshPath)
	for _, want := range []string{"Host nas\n", "Host zebra\n", "Host box\n    HostName box.lan\n"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	if !strings.Contains(a.toast.text, "Added nas, zebra, box") || len(a.cfg.Meta("box").Tags) != 0 || len(a.cfg.Meta("zebra").Tags) != 1 {
		t.Fatalf("toast %q, tags %v %v", a.toast.text, a.cfg.Meta("box"), a.cfg.Meta("zebra"))
	}
}

func TestOpenAdmin(t *testing.T) {
	var opened string
	old := openURL
	openURL = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { openURL = old })
	a := newTestApp(t, 120, 40).a
	withDevices(t, a)
	press(a, "2", "w")
	if opened != "https://login.tailscale.com/admin/machines?q=nas" {
		t.Fatal(opened)
	}
}

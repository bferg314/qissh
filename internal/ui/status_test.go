package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/qissh/internal/probe"
	"github.com/bferg314/qissh/internal/tailscale"
)

// reach feeds a reachability result for alias.
func reach(a *App, alias string, r probe.Reach) {
	a.Update(reachMsg{gen: a.checks.gen, alias: alias, reach: r})
}

func TestStatusColumn(t *testing.T) {
	a := newTestApp(t, 130, 30).a
	reach(a, "elm", probe.Reach{Up: true, Latency: 12 * time.Millisecond, At: time.Now()})
	reach(a, "oak", probe.Reach{Err: "refused", At: time.Now()})
	s := screen(a)
	for _, want := range []string{"● 12ms", "○ refused", "● via elm", "STATUS"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if b := bar(a); !strings.Contains(b, "●2 up ○1 down") {
		t.Errorf("bar: %q", b)
	}
}

func TestCheckAllSkipsJumpHosts(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	cmd := a.checkAll()
	msg := cmd()
	n := len(msg.(tea.BatchMsg))
	if n != 3 { // elm, oak, oak2; devbox1 goes through elm
		t.Fatalf("%d dials", n)
	}
}

func TestTailscaleOffline(t *testing.T) {
	a := newTestApp(t, 130, 40).a
	reach(a, "oak", probe.Reach{Err: "timeout"})
	a.Update(tailscaleMsg{peers: []*tailscale.Peer{{
		DNSName: "oak.example.ts.net.", OS: "linux", Online: false, LastSeen: time.Now().Add(-50 * time.Hour),
	}}})
	a.hosts.focus("oak")
	s := screen(a)
	if !strings.Contains(s, "○ off 2d") || !strings.Contains(s, "offline · last seen 2d ago · linux") {
		t.Fatalf("tailscale's view wins over the timeout:\n%s", s)
	}
}

func TestSortByStatus(t *testing.T) {
	a := newTestApp(t, 120, 30).a
	reach(a, "oak2", probe.Reach{Up: true, Latency: 50 * time.Millisecond})
	reach(a, "elm", probe.Reach{Up: true, Latency: 5 * time.Millisecond})
	reach(a, "oak", probe.Reach{Err: "refused"})
	press(a, "s", "s", "s")
	var order []string
	for _, r := range a.hosts.hostRows() {
		order = append(order, r.host.Alias)
	}
	// elm (5ms) and devbox1 (via elm, so up) first, then oak2, then oak (down).
	if got := strings.Join(order, ","); got != "devbox1,elm,oak2,oak" {
		t.Fatalf("order %s", got)
	}
}

func TestLoginCheck(t *testing.T) {
	e := newTestApp(t, 130, 40)
	a := e.a
	e.fake.logins["oak"] = "alice@oak.example.ts.net: Permission denied (publickey)."
	a.hosts.focus("elm")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	runCmd(a, cmd)
	if !strings.Contains(a.toast.text, "elm: login works") || !strings.Contains(screen(a), "✓ works") {
		t.Fatalf("toast %q\n%s", a.toast.text, screen(a))
	}
	login := ""
	for _, c := range *e.calls {
		if strings.HasSuffix(c, "elm exit 0") {
			login = c
		}
	}
	if !strings.Contains(login, "BatchMode=yes") || !strings.Contains(login, "ConnectTimeout=") {
		t.Fatalf("login checks must never prompt: %q", login)
	}

	a.hosts.focus("oak")
	_, cmd = a.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	runCmd(a, cmd)
	if !strings.Contains(a.toast.text, "needs a password or passphrase (alice@oak.example.ts.net: Permission denied") || !strings.Contains(screen(a), "✗ needs a password") {
		t.Fatalf("toast %q\n%s", a.toast.text, screen(a))
	}
}

const changedBanner = `@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
Host key for oak.example.ts.net has changed and you have requested strict checking.
Host key verification failed.`

func TestHostKeyChangedFix(t *testing.T) {
	e := newTestApp(t, 130, 40)
	a := e.a
	kh := filepath.FromSlash(e.fake.knownHosts)
	os.WriteFile(kh, []byte("oak.example.ts.net ssh-ed25519 AAAAOLD\n"), 0o600)
	e.fake.logins["oak"] = changedBanner

	a.hosts.focus("oak")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	runCmd(a, cmd)
	s := screen(a)
	if a.popup == nil || !strings.Contains(s, "oak's host key changed") || !strings.Contains(s, "SHA256:OLDKEY") ||
		!strings.Contains(s, "SHA256:NEWKEY") || !strings.Contains(s, "someone may be in the middle") {
		t.Fatalf("the popup shows both keys and a warning:\n%s", s)
	}
	if !strings.Contains(s, "▲ host key!") {
		t.Fatalf("the row warns too:\n%s", s)
	}

	delete(e.fake.logins, "oak") // once trusted, the login works
	_, cmd = a.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	runCmd(a, cmd)
	removed := false
	for _, c := range *e.calls {
		if strings.HasPrefix(c, "ssh-keygen -R oak.example.ts.net") {
			removed = true
		}
	}
	data, _ := os.ReadFile(kh)
	if !removed || !strings.HasSuffix(string(data), "oak.example.ts.net ssh-ed25519 AAAANEW\n") {
		t.Fatalf("old key removed: %v, known_hosts:\n%s", removed, data)
	}
	if !strings.Contains(screen(a), "✓ works") {
		t.Fatalf("the login is checked again:\n%s", screen(a))
	}
}

func TestHostKeyFixCancel(t *testing.T) {
	e := newTestApp(t, 130, 40)
	a := e.a
	e.fake.logins["oak"] = "No ED25519 host key is known for oak.example.ts.net and you have requested strict checking.\nHost key verification failed."
	a.hosts.focus("oak")
	_, cmd := a.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	runCmd(a, cmd)
	if a.popup == nil || !strings.Contains(screen(a), "isn't known yet") || strings.Contains(screen(a), "in the middle") {
		t.Fatalf("unknown key popup:\n%s", screen(a))
	}
	press(a, "esc")
	if a.popup != nil {
		t.Fatal("esc cancels")
	}
	if _, err := os.Stat(filepath.FromSlash(e.fake.knownHosts)); err == nil {
		t.Fatal("cancelling writes nothing")
	}
}

func TestSeveralLoginChecks(t *testing.T) {
	e := newTestApp(t, 130, 40)
	a := e.a
	e.fake.logins["oak"] = "Permission denied (publickey)."
	press(a, "a") // select all
	_, cmd := a.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	runCmd(a, cmd)
	if !strings.Contains(a.toast.text, "Login works on 3; not on oak") || a.popup != nil {
		t.Fatalf("toast %q", a.toast.text)
	}
}

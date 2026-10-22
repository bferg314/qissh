package status

import (
	"testing"
	"time"

	"github.com/bferg314/qissh/internal/probe"
	"github.com/bferg314/qissh/internal/resolve"
	"github.com/bferg314/qissh/internal/tailscale"
)

func TestOf(t *testing.T) {
	direct := resolve.Parse("hostname oak.lan\nport 22\n")
	jumped := resolve.Parse("hostname inner\nproxyjump me@elm:2222\n")
	proxied := resolve.Parse("hostname inner\nproxycommand nc %h %p\n")
	up := &probe.Reach{Up: true, Latency: 12 * time.Millisecond}
	down := &probe.Reach{Err: "refused"}
	offline := &tailscale.Peer{Online: false, LastSeen: time.Now().Add(-73 * time.Hour)}
	online := &tailscale.Peer{Online: true}
	changed := &probe.Login{Outcome: probe.HostKeyChanged}

	cases := []struct {
		name string
		in   Input
		kind Kind
		text string
	}{
		{"loading", Input{}, Checking, ""},
		{"not checked yet", Input{Res: direct}, Checking, ""},
		{"up", Input{Res: direct, Reach: up}, Up, "12ms"},
		{"down", Input{Res: direct, Reach: down}, Down, "refused"},
		{"tailscale offline wins", Input{Res: direct, Reach: down, Peer: offline}, Offline, "off 3d"},
		{"tailscale online defers to the check", Input{Res: direct, Reach: up, Peer: online}, Up, "12ms"},
		{"host key changed", Input{Res: direct, Reach: up, Login: changed}, Warn, "host key!"},
		{"via a jump that's up", Input{Res: jumped, Jump: &State{Kind: Up}}, Up, "via elm"},
		{"via an unknown jump", Input{Res: jumped}, Unknown, "via elm"},
		{"proxy command", Input{Res: proxied}, Unknown, "proxy"},
	}
	for _, c := range cases {
		if s := Of(c.in); s.Kind != c.kind || s.Text != c.text {
			t.Errorf("%s: got %v %q, want %v %q", c.name, s.Kind, s.Text, c.kind, c.text)
		}
	}
}

func TestFirstHop(t *testing.T) {
	cases := map[string]string{
		"elm":                 "elm",
		"me@elm:2222":         "elm",
		"elm,inner":           "elm",
		"me@[fd7a::1]:22,x":   "fd7a::1",
		"bastion.example.com": "bastion.example.com",
	}
	for in, want := range cases {
		if got := FirstHop(in); got != want {
			t.Errorf("FirstHop(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLatency(t *testing.T) {
	if Latency(400*time.Microsecond) != "<1ms" || Latency(42*time.Millisecond) != "42ms" || Latency(1500*time.Millisecond) != "1.5s" {
		t.Error(Latency(400*time.Microsecond), Latency(42*time.Millisecond), Latency(1500*time.Millisecond))
	}
}

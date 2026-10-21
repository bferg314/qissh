// Package status combines what quick-ssh knows about a host (ssh's
// resolved config, Tailscale, a TCP check, a login check) into the one
// state shown for it, so the dashboard and `qs check` agree.
package status

import (
	"fmt"
	"strings"
	"time"

	"github.com/bferg314/quick-ssh/internal/probe"
	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/tailscale"
)

// Kind is how a host is doing, best first, for sorting.
type Kind int

const (
	Up       Kind = iota
	Checking      // no answer yet
	Unknown       // can't be checked from here (ProxyCommand)
	Warn          // reachable, but the host key changed
	Down
	Offline // Tailscale says the machine is off the tailnet
)

// State is a host's status and a few words about it.
type State struct {
	Kind    Kind
	Text    string // "12ms", "refused", "off 3d", "via elm"...
	Latency time.Duration
}

// Input is what's known about one host.
type Input struct {
	Res   *resolve.Resolved
	Peer  *tailscale.Peer // nil when it isn't on the tailnet (or Tailscale is off)
	Reach *probe.Reach    // nil before the first check
	Login *probe.Login    // nil before a login check
	// Jump is the state of the first ProxyJump hop, when the host has one
	// and it's a host quick-ssh knows.
	Jump *State
}

// Of works out a host's state.
func Of(in Input) State {
	switch {
	case in.Res == nil:
		return State{Kind: Checking}
	case in.Res.Err != nil:
		return State{Kind: Unknown, Text: "config"}
	case in.Login != nil && in.Login.Outcome == probe.HostKeyChanged:
		return State{Kind: Warn, Text: "host key!"}
	}
	if in.Res.ProxyJump != "" {
		hop := FirstHop(in.Res.ProxyJump)
		if in.Jump == nil {
			return State{Kind: Unknown, Text: "via " + hop}
		}
		return State{Kind: in.Jump.Kind, Text: "via " + hop}
	}
	if in.Res.ProxyCommand != "" {
		return State{Kind: Unknown, Text: "proxy"}
	}
	if in.Peer != nil && !in.Peer.Online {
		return State{Kind: Offline, Text: "off " + Since(in.Peer.LastSeen)}
	}
	switch {
	case in.Reach == nil:
		return State{Kind: Checking}
	case in.Reach.Up:
		return State{Kind: Up, Text: Latency(in.Reach.Latency), Latency: in.Reach.Latency}
	}
	return State{Kind: Down, Text: in.Reach.Err}
}

// FirstHop is the host of the first ProxyJump hop: "elm" from
// "me@elm:2222,other".
func FirstHop(jump string) string {
	hop, _, _ := strings.Cut(jump, ",")
	if _, h, ok := strings.Cut(hop, "@"); ok {
		hop = h
	}
	if strings.HasPrefix(hop, "[") { // [v6]:port
		if end := strings.Index(hop, "]"); end > 0 {
			return hop[1:end]
		}
	}
	if h, _, ok := strings.Cut(hop, ":"); ok && strings.Count(hop, ":") == 1 {
		hop = h
	}
	return hop
}

// Latency is a short round-trip time: "4ms", "1.2s".
func Latency(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// Since is a short age: "5m", "3h", "4d", "2mo"; "?" for an unknown time.
func Since(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	switch d := time.Since(t); {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}

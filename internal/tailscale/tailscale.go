// Package tailscale reads `tailscale status --json`, which knows whether
// each machine on your tailnet is online and when it was last seen, so an
// offline host can say "offline, last seen 3d ago" instead of timing out.
package tailscale

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/bferg314/qissh/internal/run"
)

// Peer is one machine on the tailnet.
type Peer struct {
	HostName     string
	DNSName      string // MagicDNS name, e.g. "oak.example.ts.net." (with the dot)
	TailscaleIPs []string
	OS           string
	Online       bool
	LastSeen     time.Time
	Self         bool `json:"-"`
}

// Name is the peer's MagicDNS name without the trailing dot.
func (p *Peer) Name() string { return strings.TrimSuffix(strings.ToLower(p.DNSName), ".") }

// ShortName is the first label of the MagicDNS name, e.g. "oak".
func (p *Peer) ShortName() string {
	n, _, _ := strings.Cut(p.Name(), ".")
	return n
}

type status struct {
	BackendState string
	Self         *Peer
	Peer         map[string]*Peer
}

// Status lists the tailnet's machines, this one included (Self). It
// fails when Tailscale isn't running or logged in.
func Status(ctx context.Context, r run.Runner) ([]*Peer, error) {
	out, err := r.Output(ctx, "tailscale", "status", "--json")
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return Parse(out)
}

// Parse reads `tailscale status --json` output.
func Parse(out []byte) ([]*Peer, error) {
	var s status
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, err
	}
	if s.BackendState != "" && s.BackendState != "Running" {
		return nil, &NotRunningError{State: s.BackendState}
	}
	var peers []*Peer
	if s.Self != nil {
		s.Self.Self, s.Self.Online = true, true
		peers = append(peers, s.Self)
	}
	for _, p := range s.Peer {
		peers = append(peers, p)
	}
	return peers, nil
}

// NotRunningError means Tailscale is installed but not connected.
type NotRunningError struct{ State string }

func (e *NotRunningError) Error() string { return "tailscale is " + strings.ToLower(e.State) }

// Match finds the peer a host's resolved HostName refers to: its MagicDNS
// name, its short name (MagicDNS resolves "oak" too), or one of its
// Tailscale IPs.
func Match(peers []*Peer, hostname string) *Peer {
	h := strings.TrimSuffix(strings.ToLower(hostname), ".")
	if h == "" {
		return nil
	}
	for _, p := range peers {
		if p.DNSName == "" {
			continue
		}
		if h == p.Name() || !strings.Contains(h, ".") && h == p.ShortName() {
			return p
		}
		for _, ip := range p.TailscaleIPs {
			if h == ip {
				return p
			}
		}
	}
	return nil
}

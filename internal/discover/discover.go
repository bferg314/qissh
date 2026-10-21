// Package discover finds machines you could add to your ssh config but
// haven't: tailnet peers and hosts in known_hosts that no Host block
// points at.
package discover

import (
	"bufio"
	"bytes"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/bferg314/quick-ssh/internal/tailscale"
)

// KnownHost is one plain (not hashed) entry in known_hosts.
type KnownHost struct {
	Names   []string // lowercase host names and addresses, without [ ]:port
	Port    int      // 0 for the default port
	KeyType string   // ssh-ed25519, ecdsa-sha2-nistp256, ...
}

// ParseKnownHosts reads known_hosts. Hashed entries (|1|...), @cert-authority
// and @revoked lines, and wildcard patterns are left out, since none of them
// name a machine.
func ParseKnownHosts(data []byte) []KnownHost {
	var out []KnownHost
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], "@") || strings.HasPrefix(f[0], "|") {
			continue
		}
		byPort := map[int][]string{}
		var ports []int
		for _, n := range strings.Split(f[0], ",") {
			if n == "" || strings.ContainsAny(n, "*?!") {
				continue
			}
			host, port := n, 0
			if strings.HasPrefix(n, "[") {
				if i := strings.Index(n, "]:"); i > 0 {
					host = n[1:i]
					port, _ = strconv.Atoi(n[i+2:])
				}
			}
			if port == 22 {
				port = 0
			}
			if _, ok := byPort[port]; !ok {
				ports = append(ports, port)
			}
			byPort[port] = append(byPort[port], strings.ToLower(host))
		}
		for _, p := range ports {
			out = append(out, KnownHost{Names: byPort[p], Port: p, KeyType: f[1]})
		}
	}
	return out
}

// Configured is a host already in the ssh config: its alias and where
// it connects to.
type Configured struct {
	Alias    string
	HostName string
	Port     int
}

// Device is a machine that isn't in the ssh config yet.
type Device struct {
	// Name is how the machine is shown: a peer's short name, or the
	// known_hosts name.
	Name string
	// Address is what HostName would be: a peer's MagicDNS name, or the
	// known_hosts name.
	Address string
	Port    int
	// Peer is set for a tailnet machine.
	Peer *tailscale.Peer
	// KeyTypes are the host key types known_hosts has for the machine;
	// set when you've connected to it before.
	KeyTypes []string
	// Others are the machine's other names and addresses in known_hosts.
	Others []string
}

// Key identifies the device across reloads, for selecting and hiding.
func (d *Device) Key() string {
	if d.Peer != nil {
		return "tailnet:" + d.Peer.Name()
	}
	if d.Port != 0 {
		return "known:[" + d.Address + "]:" + strconv.Itoa(d.Port)
	}
	return "known:" + d.Address
}

// Source says where the device was found.
func (d *Device) Source() string {
	switch {
	case d.Peer != nil && len(d.KeyTypes) > 0:
		return "tailnet, known_hosts"
	case d.Peer != nil:
		return "tailnet"
	}
	return "known_hosts"
}

// Alias suggests an alias: a peer's short name, a hostname's first label.
// An IP address gets none.
func (d *Device) Alias() string {
	if d.Peer != nil {
		return d.Peer.ShortName()
	}
	if net.ParseIP(d.Address) != nil {
		return ""
	}
	n, _, _ := strings.Cut(d.Address, ".")
	return n
}

// skipKnown are hosts in most people's known_hosts that are services, not
// machines to log in to.
var skipKnown = map[string]bool{
	"github.com": true, "gitlab.com": true, "bitbucket.org": true, "ssh.dev.azure.com": true,
	"vs-ssh.visualstudio.com": true, "codeberg.org": true, "git.sr.ht": true, "ssh.github.com": true,
	"source.developers.google.com": true, "heroku.com": true,
	"localhost": true, "127.0.0.1": true, "::1": true,
}

// Find lists tailnet peers and known_hosts entries that no configured
// host points at. A peer counts as configured when a host's HostName is
// one of its names or addresses, or a host's alias is its short name. A
// known_hosts entry belonging to a peer is folded into the peer. Peers
// come first (online ones first), then known_hosts entries, each by name.
func Find(peers []*tailscale.Peer, known []KnownHost, hosts []Configured) []*Device {
	type target struct {
		name string
		port int
	}
	taken := map[target]bool{}
	aliases := map[string]bool{}
	usedPeers := map[*tailscale.Peer]bool{}
	for _, h := range hosts {
		aliases[strings.ToLower(h.Alias)] = true
		port := h.Port
		if port == 22 {
			port = 0
		}
		name := strings.TrimSuffix(strings.ToLower(h.HostName), ".")
		if name == "" {
			name = strings.ToLower(h.Alias)
		}
		taken[target{name, port}] = true
		if p := tailscale.Match(peers, name); p != nil {
			usedPeers[p] = true
		}
	}

	var devs []*Device
	byPeer := map[*tailscale.Peer]*Device{}
	for _, p := range peers {
		if p.Self || p.DNSName == "" || usedPeers[p] || aliases[p.ShortName()] {
			continue
		}
		d := &Device{Name: p.ShortName(), Address: p.Name(), Peer: p}
		byPeer[p] = d
		devs = append(devs, d)
	}
	slices.SortStableFunc(devs, func(a, b *Device) int {
		if a.Peer.Online != b.Peer.Online {
			if a.Peer.Online {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})

	// A MagicDNS name that's no longer a peer is a machine that left the
	// tailnet.
	var tailnets []string
	for _, p := range peers {
		if _, domain, ok := strings.Cut(p.Name(), "."); ok && !slices.Contains(tailnets, "."+domain) {
			tailnets = append(tailnets, "."+domain)
		}
	}
	gone := func(n string) bool {
		return slices.ContainsFunc(tailnets, func(s string) bool { return strings.HasSuffix(n, s) })
	}

	var others []*Device
	byName := map[target]*Device{}
	for _, k := range known {
		configured, skip := false, false
		var peer *tailscale.Peer
		for _, n := range k.Names {
			if taken[target{n, k.Port}] || k.Port == 0 && aliases[n] {
				configured = true
			}
			if skipKnown[n] {
				skip = true
			}
			if p := tailscale.Match(peers, n); p == nil && gone(n) {
				skip = true
			}
			if p := tailscale.Match(peers, n); p != nil && k.Port == 0 {
				peer = p
			}
		}
		if configured || skip || peer != nil && usedPeers[peer] {
			continue
		}
		if peer != nil {
			if d := byPeer[peer]; d != nil {
				addKeyType(d, k.KeyType)
			}
			continue
		}
		name := k.Names[0]
		for _, n := range k.Names {
			if net.ParseIP(n) == nil {
				name = n // prefer a name to an address
				break
			}
		}
		// Lines sharing a name are one machine ("box.lan,10.0.0.5" and
		// "10.0.0.5" with another key type).
		var d *Device
		for _, n := range k.Names {
			if d = byName[target{n, k.Port}]; d != nil {
				break
			}
		}
		if d == nil {
			d = &Device{Name: name, Address: name, Port: k.Port}
			others = append(others, d)
		}
		addKeyType(d, k.KeyType)
		for _, n := range k.Names {
			byName[target{n, k.Port}] = d
			if n != d.Address && !slices.Contains(d.Others, n) {
				d.Others = append(d.Others, n)
			}
		}
	}
	// Names before bare addresses.
	slices.SortStableFunc(others, func(a, b *Device) int {
		if ia, ib := net.ParseIP(a.Name) != nil, net.ParseIP(b.Name) != nil; ia != ib {
			if ib {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return append(devs, others...)
}

// Handheld reports a phone or tablet on the tailnet that you've never
// connected to: almost never running sshd.
func (d *Device) Handheld() bool {
	if d.Peer == nil || len(d.KeyTypes) > 0 {
		return false
	}
	switch strings.ToLower(d.Peer.OS) {
	case "ios", "android", "tvos":
		return true
	}
	return false
}

func addKeyType(d *Device, t string) {
	if !slices.Contains(d.KeyTypes, t) {
		d.KeyTypes = append(d.KeyTypes, t)
	}
}

// FreeAlias returns alias, or alias-2, alias-3... when taken says it's in
// use.
func FreeAlias(alias string, taken func(string) bool) string {
	if alias == "" || !taken(alias) {
		return alias
	}
	for i := 2; ; i++ {
		if a := alias + "-" + strconv.Itoa(i); !taken(a) {
			return a
		}
	}
}

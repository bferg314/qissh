package tailscale

import (
	"errors"
	"testing"
)

const sample = `{
  "BackendState": "Running",
  "Self": {"HostName": "myhost", "DNSName": "myhost.example.ts.net.", "TailscaleIPs": ["100.1.1.1"], "OS": "windows", "Online": true},
  "Peer": {
    "nodekey:a": {"HostName": "oak", "DNSName": "oak.example.ts.net.", "TailscaleIPs": ["100.1.1.4", "fd7a::4"], "OS": "linux", "Online": true},
    "nodekey:b": {"HostName": "windev", "DNSName": "windev.example.ts.net.", "TailscaleIPs": ["100.1.1.9"], "OS": "windows", "Online": false, "LastSeen": "2026-10-14T05:30:27.1Z"},
    "nodekey:c": {"HostName": "localhost", "DNSName": "iphone.example.ts.net.", "OS": "iOS", "Online": true}
  }
}`

func TestParseAndMatch(t *testing.T) {
	peers, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 4 || !peers[0].Self {
		t.Fatalf("peers: %d, self first: %+v", len(peers), peers[0])
	}
	cases := map[string]string{
		"oak.example.ts.net":  "oak",
		"OAK.example.ts.net.": "oak",
		"oak":                 "oak", // MagicDNS short name
		"100.1.1.9":           "windev",
		"fd7a::4":             "oak",
		"localhost":           "", // HostName isn't used: phones report "localhost"
		"oak.lan":             "",
		"":                    "",
	}
	for host, want := range cases {
		got := ""
		if p := Match(peers, host); p != nil {
			got = p.ShortName()
		}
		if got != want {
			t.Errorf("Match(%q) = %q, want %q", host, got, want)
		}
	}
	if w := Match(peers, "windev"); w.Online || w.LastSeen.Year() != 2026 {
		t.Errorf("windev: %+v", w)
	}
}

func TestNotRunning(t *testing.T) {
	_, err := Parse([]byte(`{"BackendState": "Stopped"}`))
	var nr *NotRunningError
	if !errors.As(err, &nr) || err.Error() != "tailscale is stopped" {
		t.Fatalf("got %v", err)
	}
}

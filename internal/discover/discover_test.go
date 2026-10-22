package discover

import (
	"strings"
	"testing"

	"github.com/bferg314/qissh/internal/tailscale"
)

const knownHosts = `# a comment
oak.example.ts.net ssh-ed25519 AAAA1
box.lan,10.0.0.5 ssh-ed25519 AAAA2
10.0.0.5 ecdsa-sha2-nistp256 AAAA3
[nas.lan]:2222 ssh-ed25519 AAAA4
[nas.lan]:22 ssh-rsa AAAA5
|1|hashed=|salt= ssh-ed25519 AAAA6
@cert-authority *.corp ssh-ed25519 AAAA7
github.com ssh-ed25519 AAAA8
100.1.1.9 ssh-ed25519 AAAA9
elm.lan ssh-ed25519 AAAA10
*.wild ssh-ed25519 AAAA11
10.9.9.9 ssh-ed25519 AAAA12
gone.example.ts.net ssh-ed25519 AAAA13

short
`

func TestParseKnownHosts(t *testing.T) {
	ks := ParseKnownHosts([]byte(knownHosts))
	var got []string
	for _, k := range ks {
		got = append(got, strings.Join(k.Names, ",")+":"+string(rune('0'+k.Port/1000))+" "+k.KeyType)
	}
	want := []string{
		"oak.example.ts.net:0 ssh-ed25519",
		"box.lan,10.0.0.5:0 ssh-ed25519",
		"10.0.0.5:0 ecdsa-sha2-nistp256",
		"nas.lan:2 ssh-ed25519",
		"nas.lan:0 ssh-rsa", // [host]:22 is the default port
		"github.com:0 ssh-ed25519",
		"100.1.1.9:0 ssh-ed25519",
		"elm.lan:0 ssh-ed25519",
		"10.9.9.9:0 ssh-ed25519",
		"gone.example.ts.net:0 ssh-ed25519",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

func TestFind(t *testing.T) {
	peers := []*tailscale.Peer{
		{DNSName: "myhost.example.ts.net.", Self: true, Online: true},
		{DNSName: "oak.example.ts.net.", TailscaleIPs: []string{"100.1.1.4"}, Online: true},
		{DNSName: "windev.example.ts.net.", TailscaleIPs: []string{"100.1.1.9"}, OS: "windows"},
		{DNSName: "zebra.example.ts.net.", Online: true},
		{DNSName: "devbox1.example.ts.net.", Online: true},
		{DNSName: "nas.example.ts.net.", Online: true},
		{DNSName: "phone.example.ts.net.", OS: "iOS", Online: true},
	}
	hosts := []Configured{
		{Alias: "pi", HostName: "100.1.1.4"},             // oak by its Tailscale IP
		{Alias: "devbox1", HostName: "192.168.1.20"},     // by alias = short name
		{Alias: "elm", HostName: "elm.lan", Port: 22},    // elm.lan is configured
		{Alias: "nasbox", HostName: "nas.lan", Port: 22}, // nas.lan:22, not :2222
	}
	devs := Find(peers, ParseKnownHosts([]byte(knownHosts)), hosts)
	var got []string
	for _, d := range devs {
		got = append(got, d.Key()+" "+d.Source()+" "+strings.Join(d.KeyTypes, ","))
	}
	want := []string{
		"tailnet:nas.example.ts.net tailnet ",
		"tailnet:phone.example.ts.net tailnet ",
		"tailnet:zebra.example.ts.net tailnet ",
		"tailnet:windev.example.ts.net tailnet, known_hosts ssh-ed25519", // 100.1.1.9; offline, so after the online ones
		"known:box.lan known_hosts ssh-ed25519,ecdsa-sha2-nistp256",
		"known:[nas.lan]:2222 known_hosts ssh-ed25519",
		"known:10.9.9.9 known_hosts ssh-ed25519", // addresses after names; gone.example.ts.net left the tailnet
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	box := devs[4]
	if box.Alias() != "box" || strings.Join(box.Others, ",") != "10.0.0.5" {
		t.Fatalf("box: alias %q, others %q", box.Alias(), box.Others)
	}
	if !devs[1].Handheld() || devs[0].Handheld() {
		t.Fatal("phones are handheld")
	}
	if devs[0].Alias() != "nas" || (&Device{Address: "10.0.0.7"}).Alias() != "" {
		t.Fatal("aliases")
	}
}

func TestFreeAlias(t *testing.T) {
	taken := map[string]bool{"nas": true, "nas-2": true}
	if got := FreeAlias("nas", func(a string) bool { return taken[a] }); got != "nas-3" {
		t.Fatal(got)
	}
	if got := FreeAlias("box", func(a string) bool { return taken[a] }); got != "box" {
		t.Fatal(got)
	}
}

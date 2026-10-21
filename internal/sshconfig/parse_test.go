package sshconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// messy covers what real files contain: mixed keyword case, key=value,
// quoting, tabs, trailing comments, several names on one Host line,
// patterns, Match, and Windows paths.
const messy = `# my hosts
	# indented comment

Host elm
    HostName 10.0.0.5
    User me

host oak oak-alias
  hostname oak.example.ts.net
  user=alice   # trailing comment
	Port   2222

Host "quoted name"
    IdentityFile "C:\Users\me\.ssh\id ed25519"
    IdentityFile C:\Users\me\.ssh\id_rsa

Host *.lan !gw.lan
    ForwardAgent yes

Match host elm exec "test -f ~/x"
    User other

HOST=devbox1
HostName = devbox1.example.ts.net
Host *
    ServerAliveInterval 30`

func TestRoundTrip(t *testing.T) {
	cases := map[string]string{
		"lf":              messy + "\n",
		"crlf":            strings.ReplaceAll(messy, "\n", "\r\n") + "\r\n",
		"no final eol":    messy,
		"bom":             "\xEF\xBB\xBF" + messy + "\n",
		"mixed endings":   "Host a\r\n  HostName x\nHost b\n",
		"empty":           "",
		"only blank":      "\n\n\n",
		"trailing spaces": "Host a   \n  User b \t\n",
	}
	for name, in := range cases {
		if got := string(Parse("x", []byte(in)).Bytes()); got != in {
			t.Errorf("%s: round trip changed the file:\n got %q\nwant %q", name, got, in)
		}
	}
}

func TestParseLine(t *testing.T) {
	cases := []struct {
		in      string
		keyword string
		args    []string
	}{
		{"  HostName example.com", "hostname", []string{"example.com"}},
		{"user=alice", "user", []string{"alice"}},
		{"User = alice", "user", []string{"alice"}},
		{"Port   2222  # ssh on a side port", "port", []string{"2222"}},
		{`IdentityFile "C:\Users\me\my key"`, "identityfile", []string{`C:\Users\me\my key`}},
		{`IdentityFile C:\Users\me\.ssh\id_rsa`, "identityfile", []string{`C:\Users\me\.ssh\id_rsa`}},
		{`ProxyCommand ssh -W %h:%p 'jump host'`, "proxycommand", []string{"ssh", "-W", "%h:%p", "jump host"}},
		{`Host a\ b`, "host", []string{"a b"}},
		{"Host a#b", "host", []string{"a#b"}},
		{"# comment", "", nil},
		{"   ", "", nil},
		{"Compression", "compression", nil},
	}
	for _, c := range cases {
		l := Line{Text: c.in}
		parseLine(&l)
		if l.Keyword != c.keyword || !reflect.DeepEqual(l.Args, c.args) {
			t.Errorf("parseLine(%q) = %q %q, want %q %q", c.in, l.Keyword, l.Args, c.keyword, c.args)
		}
	}
}

func TestBlocks(t *testing.T) {
	f := Parse("x", []byte(messy))
	var kinds []Kind
	for _, b := range f.Blocks {
		kinds = append(kinds, b.Kind)
	}
	want := []Kind{Preamble, HostBlock, HostBlock, HostBlock, HostBlock, MatchBlock, HostBlock, HostBlock}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("blocks %v, want %v", kinds, want)
	}
	if b := f.Blocks[0]; b.Start != 0 || b.End != 3 {
		t.Errorf("preamble %+v", b)
	}
	if f.Blocks[len(f.Blocks)-1].End != len(f.Lines) {
		t.Error("the last block runs to the end of the file")
	}
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func aliases(c *Config) string {
	var names []string
	for _, h := range c.Hosts {
		names = append(names, h.Alias)
	}
	return strings.Join(names, ",")
}

func TestLoadHostsAndRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, messy+"\nHost ELM\n  Port 2200\n")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := aliases(c); got != "elm,oak,oak-alias,quoted name,devbox1" {
		t.Fatalf("hosts %s", got)
	}
	oak := c.Find("OAK")
	if oak == nil || oak.Value("hostname") != "oak.example.ts.net" || oak.Value("user") != "alice" || oak.Value("port") != "2222" {
		t.Fatalf("oak: %+v", oak)
	}
	if !reflect.DeepEqual(oak.Siblings, []string{"oak-alias"}) || oak.LineNo() != 8 {
		t.Errorf("oak siblings %q line %d", oak.Siblings, oak.LineNo())
	}
	if elm := c.Find("elm"); len(elm.Also) != 1 || elm.Also[0].LineNo() != 27 {
		t.Errorf("a second Host ELM block is recorded on the first: %+v", elm.Also)
	}
	if q := c.Find("quoted name"); q == nil || len(must(q.Get("identityfile"))) != 1 {
		t.Errorf("quoted host: %+v", q)
	}

	var rules []string
	for _, r := range c.Rules {
		rules = append(rules, r.Text())
	}
	want := []string{"Host *.lan !gw.lan", `Match host elm exec test -f ~/x`, "Host *"}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("rules %q, want %q", rules, want)
	}
}

func must(args []string, _ bool) []string { return args }

func TestLoadMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(c.Hosts) != 0 {
		t.Fatalf("a missing config is empty: %v %+v", err, c)
	}
}

func TestInclude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ssh := filepath.Join(home, ".ssh")
	writeFile(t, filepath.Join(ssh, "config"), "Include conf.d/*\nHost first\n  HostName 1\nInclude ~/.ssh/extra missing/*\nHost *\n  User x\n")
	writeFile(t, filepath.Join(ssh, "conf.d", "b"), "Host b1\n")
	writeFile(t, filepath.Join(ssh, "conf.d", "a"), "Host a1\nInclude ../config\n") // a loop: ignored
	writeFile(t, filepath.Join(ssh, "extra"), "Host extra first\n  HostName later\n")

	c, err := Load(filepath.Join(ssh, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if got := aliases(c); got != "a1,b1,first,extra" {
		t.Fatalf("hosts in ssh's reading order: %s", got)
	}
	if len(c.Files) != 4 || len(c.Warnings) != 0 {
		t.Fatalf("files %d warnings %q", len(c.Files), c.Warnings)
	}
	first := c.Find("first")
	if first.File.Path != filepath.Join(ssh, "config") || len(first.Also) != 1 || filepath.Base(first.Also[0].File.Path) != "extra" {
		t.Errorf("first: %s also %+v", first.File.Path, first.Also)
	}
	if e := c.Find("extra"); filepath.Base(e.File.Path) != "extra" {
		t.Errorf("extra is defined in the included file: %s", e.File.Path)
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		patterns []string
		name     string
		want     bool
	}{
		{[]string{"*"}, "anything", true},
		{[]string{"*.lan"}, "pi.lan", true},
		{[]string{"*.lan"}, "pi.lan.example", false},
		{[]string{"oa?"}, "oak", true},
		{[]string{"oa?"}, "oak22", false},
		{[]string{"*.lan", "!gw.lan"}, "gw.lan", false},
		{[]string{"!gw.lan", "*.lan"}, "gw.lan", false},
		{[]string{"!gw.lan"}, "other", false},
		{[]string{"ELM"}, "elm", true},
		{[]string{"a*b*c"}, "aXXbYc", true},
	}
	for _, c := range cases {
		if got := Matches(c.patterns, c.name); got != c.want {
			t.Errorf("Matches(%q, %q) = %v", c.patterns, c.name, got)
		}
	}
}

func TestSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, `Host *.lan
    User lanuser
Host oak
    HostName oak.lan
    User me
Match exec "true"
    Port 2200
Host oak
    Compression yes
Match all
    ServerAliveInterval 5
Host *
    User fallback
    ForwardAgent no
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	oak := c.Find("oak")
	where := func(kw string) string {
		loc, ok := c.Source(oak, kw)
		if !ok {
			return "default"
		}
		return fmt.Sprintf("%s:%d", loc.Text(), loc.LineNo())
	}
	cases := map[string]string{
		"user":                "Host oak:3", // *.lan doesn't match the alias "oak"
		"hostname":            "Host oak:3",
		"compression":         "Host oak:8", // the second block for oak
		"serveraliveinterval": "Match all:10",
		"forwardagent":        "Host *:12",
		"port":                "default", // only an exec Match sets it
	}
	for kw, want := range cases {
		if got := where(kw); got != want {
			t.Errorf("%s from %s, want %s", kw, got, want)
		}
	}
}

// A host first defined in an included file and named again later is one
// host, not two.
func TestIncludedHostDefinedAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ssh := filepath.Join(home, ".ssh")
	writeFile(t, filepath.Join(ssh, "config"), "Include extra\nHost box\n  Port 2\n")
	writeFile(t, filepath.Join(ssh, "extra"), "Host box\n  HostName b\n")
	c, err := Load(filepath.Join(ssh, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if got := aliases(c); got != "box" || len(c.Find("box").Also) != 1 {
		t.Fatalf("hosts %s, also %+v", got, c.Find("box").Also)
	}
}

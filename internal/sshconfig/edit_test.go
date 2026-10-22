package sshconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const editBase = `# my hosts

# the pi on the shelf
host oak oak2
  hostname oak.lan   # tailnet name
  user=alice

Host elm
    HostName 10.0.0.5
    User root
    IdentityFile ~/.ssh/a
    IdentityFile ~/.ssh/b

# defaults
Host *
    ServerAliveInterval 30
`

func hostBlock(t *testing.T, f *File, alias string) int {
	t.Helper()
	for i, b := range f.Blocks {
		if b.Kind != HostBlock {
			continue
		}
		for _, a := range f.Lines[b.Start].Args {
			if a == alias {
				return i
			}
		}
	}
	t.Fatalf("no block for %s", alias)
	return -1
}

func TestSetHostChangesOnlyWhatChanged(t *testing.T) {
	f := Parse("x", []byte(editBase))
	err := f.SetHost(hostBlock(t, f, "oak"), "oak", HostFields{
		Alias: "pi5", HostName: "pi5.lan", User: "alice", Port: "2222", IdentityFile: `C:\Users\me\my key`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, `host oak oak2
  hostname oak.lan   # tailnet name
  user=alice
`, `host pi5 oak2
  hostname pi5.lan   # tailnet name
  user=alice
  port 2222
  identityfile "C:\Users\me\my key"
`, 1)
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// And ssh reads the quoted path back as it was typed.
	again := Parse("x", f.Bytes())
	if args := again.Lines[7].Args; len(args) != 1 || args[0] != `C:\Users\me\my key` {
		t.Fatalf("round trip of the path: %q", args)
	}
}

func TestSetHostRemovesClearedOptions(t *testing.T) {
	f := Parse("x", []byte(editBase))
	if err := f.SetHost(hostBlock(t, f, "elm"), "elm", HostFields{Alias: "elm", HostName: "10.0.0.6"}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, `    HostName 10.0.0.5
    User root
    IdentityFile ~/.ssh/a
    IdentityFile ~/.ssh/b
`, `    HostName 10.0.0.6
`, 1)
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddHostGoesBeforeDefaults(t *testing.T) {
	f := Parse("x", []byte(editBase))
	f.AddHost(HostFields{Alias: "newbox", HostName: "n.lan", User: "me"})
	want := strings.Replace(editBase, "# defaults\nHost *", "Host newbox\n    HostName n.lan\n    User me\n\n# defaults\nHost *", 1)
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddHostAtEnd(t *testing.T) {
	cases := map[string]string{
		"":                                 "Host a\n    HostName h\n",
		"Host x\n    User y":               "Host x\n    User y\n\nHost a\n    HostName h\n",
		"Host x\r\n    User y\r\n":         "Host x\r\n    User y\r\n\r\nHost a\r\n    HostName h\r\n",
		"host x\n  user y\n\n":             "host x\n  user y\n\nhost a\n    hostname h\n",
		"host x\n  hostname z\n  user y\n": "host x\n  hostname z\n  user y\n\nhost a\n    hostname h\n",
	}
	for in, want := range cases {
		f := Parse("x", []byte(in))
		f.AddHost(HostFields{Alias: "a", HostName: "h"})
		if got := string(f.Bytes()); got != want {
			t.Errorf("from %q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRemoveHost(t *testing.T) {
	f := Parse("x", []byte(editBase))
	if err := f.RemoveHost(hostBlock(t, f, "oak2"), "oak2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.Bytes()), "host oak\n  hostname") {
		t.Fatalf("a shared block keeps its other name:\n%s", f.Bytes())
	}
	if err := f.RemoveHost(hostBlock(t, f, "oak"), "oak"); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editBase, "# the pi on the shelf\nhost oak oak2\n  hostname oak.lan   # tailnet name\n  user=alice\n\n", "", 1)
	if got := string(f.Bytes()); got != want {
		t.Fatalf("the block and its comment go:\n%s\nwant:\n%s", got, want)
	}

	// The last block in a file without a final newline.
	f = Parse("x", []byte("Host a\n  User b\n\nHost c\n  User d"))
	f.RemoveHost(hostBlock(t, f, "c"), "c")
	if got := string(f.Bytes()); got != "Host a\n  User b" {
		t.Fatalf("got %q", got)
	}
	// A comment that opens the file stays.
	f = Parse("x", []byte("# header\nHost a\n  User b\nHost c\n"))
	f.RemoveHost(hostBlock(t, f, "a"), "a")
	if got := string(f.Bytes()); got != "# header\nHost c\n" {
		t.Fatalf("got %q", got)
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, editBase)
	backups := filepath.Join(dir, "backups")
	data, _ := os.ReadFile(path)

	f := Parse(path, data)
	f.AddHost(HostFields{Alias: "n", HostName: "n.lan"})
	var checked []string
	ok := func(p string) error { checked = append(checked, p); return nil }
	backup, err := Save(f, ok, backups)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "Host n\n") {
		t.Fatalf("not written:\n%s", got)
	}
	if b, _ := os.ReadFile(backup); string(b) != editBase {
		t.Fatal("backup should hold the old content")
	}
	if b, _ := os.ReadFile(path + ".qs-bak"); string(b) != editBase {
		t.Fatal("the .qs-bak beside the file should hold the old content")
	}
	if len(checked) != 2 || checked[0] == path || checked[1] != path {
		t.Fatalf("validated the temp file, then the real one: %q", checked)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Fatalf("no temp files left behind: %v", entries)
	}

	// Saving the stale parse again is refused.
	if _, err := Save(f, ok, backups); !errors.Is(err, ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}

	// A config ssh rejects is never written.
	now, _ := os.ReadFile(path)
	g := Parse(path, now)
	g.AddHost(HostFields{Alias: "bad"})
	_, err = Save(g, func(string) error { return errors.New("Bad configuration option") }, backups)
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("got %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(now) {
		t.Fatal("the file must be unchanged")
	}
}

func TestSaveNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh", "config")
	f := Parse(path, nil)
	f.AddHost(HostFields{Alias: "a", HostName: "h"})
	if _, err := Save(f, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "Host a\n    HostName h\n" {
		t.Fatalf("got %q", got)
	}
}

func TestBackupsArePruned(t *testing.T) {
	dir := t.TempDir()
	for range keepBackups + 3 {
		if _, err := writeBackup(dir, "/x/config", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != keepBackups {
		t.Fatalf("%d backups kept", len(entries))
	}
}

func TestParseTarget(t *testing.T) {
	cases := []struct{ in, user, host, port string }{
		{"oak.lan", "", "oak.lan", ""},
		{"me@oak.lan", "me", "oak.lan", ""},
		{"me@oak.lan:2222", "me", "oak.lan", "2222"},
		{"10.0.0.5:22", "", "10.0.0.5", "22"},
		{"fe80::1", "", "fe80::1", ""},
		{"root@[fe80::1]:2222", "root", "fe80::1", "2222"},
		{"[::1]", "", "::1", ""},
		{"a@b@c", "a@b", "c", ""},
	}
	for _, c := range cases {
		user, host, port, err := ParseTarget(c.in)
		if err != nil || user != c.user || host != c.host || port != c.port {
			t.Errorf("%q: got %q %q %q %v", c.in, user, host, port, err)
		}
	}
	for _, bad := range []string{"", "@h", "me@", "h:", "[::1", "[::1]x", "[::1]:", "me@:22"} {
		if _, _, _, err := ParseTarget(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestCheckFields(t *testing.T) {
	cases := []struct {
		f     HostFields
		field Field
		want  string
	}{
		{HostFields{}, FieldAlias, "Give the host an alias"},
		{HostFields{Alias: "my box"}, FieldAlias, "spaces"},
		{HostFields{Alias: "web*"}, FieldAlias, "pattern"},
		{HostFields{Alias: "!x"}, FieldAlias, "pattern"},
		{HostFields{Alias: "a", HostName: "h h"}, FieldHostName, "hostname"},
		{HostFields{Alias: "a", User: "u u"}, FieldUser, "user"},
		{HostFields{Alias: "a", Port: "0"}, FieldPort, "Port"},
		{HostFields{Alias: "a", Port: "x"}, FieldPort, "Port"},
		{HostFields{Alias: "a", ProxyJump: "a b"}, FieldProxyJump, "ProxyJump"},
	}
	for _, c := range cases {
		e := c.f.Check()
		if e == nil || e.Field != c.field || !strings.Contains(e.Msg, c.want) {
			t.Errorf("%+v: got %+v", c.f, e)
		}
	}
	if e := (HostFields{Alias: "a", HostName: "h", User: "u", Port: "65535", ProxyJump: "j"}).Check(); e != nil {
		t.Errorf("good fields: %v", e)
	}
}

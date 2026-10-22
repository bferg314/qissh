package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bferg314/qissh/internal/sshconfig"
)

func TestParseAdd(t *testing.T) {
	cases := map[string]sshconfig.HostFields{
		"box me@box.lan":                      {Alias: "box", HostName: "box.lan", User: "me"},
		"box me@box.lan:2222 -i ~/.ssh/k":     {Alias: "box", HostName: "box.lan", User: "me", Port: "2222", IdentityFile: "~/.ssh/k"},
		"-J elm box 10.0.0.9":                 {Alias: "box", HostName: "10.0.0.9", ProxyJump: "elm"},
		"box -i k root@[fe80::1]:22 -J a@b:1": {Alias: "box", HostName: "fe80::1", User: "root", Port: "22", IdentityFile: "k", ProxyJump: "a@b:1"},
	}
	for in, want := range cases {
		got, err := parseAdd(strings.Fields(in))
		if err != nil || got != want {
			t.Errorf("%q: got %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "box", "box h extra", "box me@", "box h -x"} {
		if _, err := parseAdd(strings.Fields(bad)); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// A heavily commented config, in the styles people write by hand.
const commented = "# ~/.ssh/config\r\n" +
	"#   managed by hand; keep it tidy\r\n" +
	"\r\n" +
	"Include conf.d/*   # work hosts\r\n" +
	"\r\n" +
	"# --- home ---\r\n" +
	"host oak oak2\r\n" +
	"\thostname=oak.lan\t# shelf\r\n" +
	"\tuser alice\r\n" +
	"\r\n" +
	"Host elm # the NAS\r\n" +
	"    HostName \"10.0.0.5\"\r\n" +
	"    # root only for now\r\n" +
	"    User root\r\n" +
	"\r\n" +
	"# --- defaults: keep last ---\r\n" +
	"Host *\r\n" +
	"    ServerAliveInterval 30\r\n" +
	"    # AddKeysToAgent yes"

func TestAddHostChangesOnlyItsLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(commented), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh, err := sshconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var checked []string
	validate := func(p string) error { checked = append(checked, p); return nil }
	var out strings.Builder
	fields := sshconfig.HostFields{Alias: "box", HostName: "box.lan", User: "me", Port: "2222", IdentityFile: "~/.ssh/no such key"}
	if err := addHost(&out, ssh, fields, validate, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(path)
	want := strings.Replace(commented, "# --- defaults", "Host box\r\n"+
		"    HostName box.lan\r\n"+
		"    User me\r\n"+
		"    Port 2222\r\n"+
		"    IdentityFile \"~/.ssh/no such key\"\r\n"+
		"\r\n"+
		"# --- defaults", 1)
	if string(got) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	if len(checked) != 2 {
		t.Errorf("ssh checks the temp file and then the real one: %v", checked)
	}
	if bak, _ := os.ReadFile(path + ".qs-bak"); string(bak) != commented {
		t.Errorf("backup beside it: %q", bak)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "backups")); len(entries) != 1 {
		t.Errorf("one backup: %v", entries)
	}
	if s := out.String(); !strings.Contains(s, "added box (me@box.lan:2222)") || !strings.Contains(s, "doesn't exist yet") {
		t.Errorf("output: %q", s)
	}
}

func TestAddHostRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	orig := "Host elm\n    HostName 10.0.0.5\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh, err := sshconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ok := func(string) error { return nil }
	cases := []struct {
		fields   sshconfig.HostFields
		validate sshconfig.Validator
		want     string
	}{
		{sshconfig.HostFields{Alias: "ELM", HostName: "x"}, ok, "elm already exists"},
		{sshconfig.HostFields{Alias: "web*", HostName: "x"}, ok, "pattern"},
		{sshconfig.HostFields{Alias: "box", HostName: "x", Port: "99999"}, ok, "Port"},
		{sshconfig.HostFields{Alias: "box", HostName: "x"}, func(string) error { return errors.New("line 4: Bad configuration option") }, "Bad configuration option"},
	}
	for _, c := range cases {
		err := addHost(&strings.Builder{}, ssh, c.fields, c.validate, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.fields, err, c.want)
		}
		if got, _ := os.ReadFile(path); string(got) != orig {
			t.Fatalf("%+v: the file changed:\n%s", c.fields, got)
		}
	}
}

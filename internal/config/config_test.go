package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakePath makes a directory with fake executables for names and puts only
// that directory on PATH.
func fakePath(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	fakeInstall(t, dir, names...)
	t.Setenv("PATH", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".EXE")
	}
	return dir
}

func fakeInstall(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		file := filepath.Join(dir, n)
		if runtime.GOOS == "windows" {
			file += ".exe"
		}
		if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadCreatesDefaults(t *testing.T) {
	fakePath(t, "sftp")
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.AliasName != "qs" || c.ConnectMode != ConnectAuto || c.SSHConfig != DefaultSSHConfig {
		t.Fatalf("defaults: %+v", c)
	}
	for _, tool := range c.Tools {
		if want := tool.Cmd == "sftp"; tool.Enabled != want || tool.AutoDisabled == want {
			t.Errorf("%s: enabled=%v auto=%v", tool.Name, tool.Enabled, tool.AutoDisabled)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("config not written:", err)
	}
}

func TestLoadFillsBadValues(t *testing.T) {
	fakePath(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("connect_mode = \"teleport\"\nalias_name = \"\"\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ConnectMode != ConnectAuto || c.AliasName != "qs" {
		t.Fatalf("got mode %q alias %q", c.ConnectMode, c.AliasName)
	}
}

func TestEnableInstalled(t *testing.T) {
	dir := fakePath(t, "sftp")
	c := &Config{Tools: []Tool{{Name: "SFTP", Cmd: "sftp"}, {Name: "mosh", Cmd: "mosh"}, {Name: "VS Code", Cmd: "code"}}}
	c.DetectTools()
	c.Tools[2].AutoDisabled = false // turned off by hand
	fakeInstall(t, dir, "mosh", "code")
	if got := c.EnableInstalled(); strings.Join(got, ",") != "mosh" || c.Tools[2].Enabled {
		t.Fatalf("got %v, tools %+v", got, c.Tools)
	}
}

func TestMarks(t *testing.T) {
	c := &Config{}
	c.SetPinned("Oak", true)
	c.SetPinned("oak", true)
	if len(c.Pinned) != 1 {
		t.Fatalf("aliases ignore case: %q", c.Pinned)
	}
	c.SetHidden("oak", true)
	if len(c.Pinned) != 0 || len(c.Hidden) != 1 {
		t.Fatalf("hiding unpins: %q %q", c.Pinned, c.Hidden)
	}
	if _, hidden := c.HostMarks(); !hidden["oak"] {
		t.Fatal("HostMarks should report the hidden host")
	}
}

func TestCustomSSHConfig(t *testing.T) {
	if (&Config{SSHConfig: "~/.ssh/config"}).CustomSSHConfig() {
		t.Error("the default config needs no -F")
	}
	if !(&Config{SSHConfig: "~/other/config"}).CustomSSHConfig() {
		t.Error("another file needs -F")
	}
}

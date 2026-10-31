package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/history"
	"github.com/bferg314/qissh/internal/sshconfig"
	"github.com/bferg314/qissh/internal/ui"
)

const hostsConfig = "Host bastion\n    HostName bastion.example.com\n\n" +
	"Host web1 web2\n    HostName web.lan\n    ProxyJump bastion\n\n" +
	"Host db\n    HostName db.internal\n\n" +
	"Host garage-pi\n    HostName garage-pi.example.ts.net\n\n" +
	"Host *\n    ServerAliveInterval 30\n"

// testSetup loads hostsConfig as the ssh config, with an empty history,
// both in a temp folder.
func testSetup(t *testing.T) *setup {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(hostsConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh, err := sshconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SSHConfig = path
	return &setup{cfg: cfg, hist: history.Load(filepath.Join(dir, "history.json")), ssh: ssh}
}

func TestPick(t *testing.T) {
	s := testSetup(t)
	cases := map[string]string{
		"web1":          "web1",      // exact
		"WEB2":          "web2",      // aliases match whatever the case
		"gar":           "garage-pi", // the only fuzzy match
		"web":           "",          // two matches: the dashboard, filtered
		"zzz":           "",          // none
		"me@other.lan":  "me@other.lan",
		"other.lan:22":  "other.lan:22",
		"bastion.local": "bastion.local", // looks like a hostname, matches no alias
	}
	for query, want := range cases {
		if got, err := pick(s, query); err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", query, got, err, want)
		}
	}
}

func TestPickLast(t *testing.T) {
	s := testSetup(t)
	if _, err := pick(s, "-"); err == nil {
		t.Fatal("with no history, - should be an error")
	}
	_ = s.hist.Record("db")
	if got, err := pick(s, "-"); err != nil || got != "db" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestConnectNowViaShell(t *testing.T) {
	s := testSetup(t)
	s.cfg.ConnectMode = config.ConnectShell
	cmdFile := filepath.Join(t.TempDir(), "cmd")

	done, err := connectNow(s, "gar", cmdFile)
	if err != nil || !done {
		t.Fatalf("got %v %v", done, err)
	}
	got, _ := os.ReadFile(cmdFile)
	want := "ssh\n-F\n" + s.cfg.SSHConfigPath() + "\ngarage-pi\n"
	if string(got) != want {
		t.Fatalf("cmd file: got %q, want %q", got, want)
	}
	if s.hist.Last() != "garage-pi" || s.hist.Get("garage-pi").Count != 1 {
		t.Fatalf("the connection should be recorded: %+v", s.hist)
	}
	histPath := filepath.Join(filepath.Dir(s.cfg.SSHConfigPath()), "history.json")
	if again := history.Load(histPath); again.Last() != "garage-pi" {
		t.Fatal("the history should be saved")
	}
}

func TestConnectNowOpensDashboard(t *testing.T) {
	s := testSetup(t)
	s.cfg.ConnectMode = config.ConnectShell
	cmdFile := filepath.Join(t.TempDir(), "cmd")
	for _, query := range []string{"", "web", "zzz"} {
		if done, err := connectNow(s, query, cmdFile); done || err != nil {
			t.Errorf("%q: got %v %v", query, done, err)
		}
	}
	if _, err := os.Stat(cmdFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nothing should be written to the cmd file")
	}
	if s.hist.Last() != "" {
		t.Fatal("nothing should be recorded")
	}
	if done, err := connectNow(s, "-", cmdFile); done || err == nil {
		t.Fatalf("- with no history: got %v %v", done, err)
	}
}

func TestFinishViaShell(t *testing.T) {
	cmdFile := filepath.Join(t.TempDir(), "cmd")
	o := ui.Outcome{Argv: []string{"ssh", "-F", `C:\my configs\ssh`, "web1"}, ViaShell: true}
	if err := finish(o, cmdFile); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cmdFile)
	if want := "ssh\n-F\nC:\\my configs\\ssh\nweb1\n"; string(got) != want {
		t.Fatalf("one argument per line, spaces kept: got %q", got)
	}
	if fi, _ := os.Stat(cmdFile); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestFinishNothingChosen(t *testing.T) {
	cmdFile := filepath.Join(t.TempDir(), "cmd")
	if err := finish(ui.Outcome{ViaShell: true}, cmdFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cmdFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quitting the dashboard without connecting writes nothing")
	}
}

// TestHelperProcess stands in for ssh in TestFinishExitCode: it exits with
// QISSH_HELPER_EXIT.
func TestHelperProcess(t *testing.T) {
	code := os.Getenv("QISSH_HELPER_EXIT")
	if code == "" {
		return
	}
	n, _ := strconv.Atoi(code)
	os.Exit(n)
}

func TestFinishExitCode(t *testing.T) {
	argv := []string{os.Args[0], "-test.run=^TestHelperProcess$"}
	for _, code := range []int{0, 1, 255} {
		t.Setenv("QISSH_HELPER_EXIT", strconv.Itoa(code))
		err := finish(ui.Outcome{Argv: argv}, "")
		var exit exitError
		switch {
		case code == 0 && err != nil:
			t.Errorf("exit 0: got %v", err)
		case code != 0 && (!errors.As(err, &exit) || int(exit) != code):
			t.Errorf("exit %d: got %v", code, err)
		}
	}
	if err := finish(ui.Outcome{Argv: []string{"qissh-no-such-program"}}, ""); err == nil || strings.HasPrefix(err.Error(), "exit status") {
		t.Errorf("a missing program is an error of its own: %v", err)
	}
}

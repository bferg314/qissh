package remote

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bferg314/qissh/internal/resolve"
)

type exitErr int

func (e exitErr) Error() string { return "exit status" }
func (e exitErr) ExitCode() int { return int(e) }

// fake answers like ssh: the last argument is the command.
type fake struct {
	out string
	err error
	got []string
}

func (f *fake) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.Combined(ctx, name, args...)
}

func (f *fake) Combined(_ context.Context, name string, args ...string) ([]byte, error) {
	f.got = append([]string{name}, args...)
	return []byte(f.out), f.err
}

func TestRun(t *testing.T) {
	f := &fake{out: "hello\n"}
	r := Run(context.Background(), f, resolve.Options{}, "oak", "echo hello", time.Second)
	if !r.OK() || r.Output != "hello\n" || strings.Join(f.got, " ") != "ssh -o BatchMode=yes -o ConnectTimeout=8 oak echo hello" {
		t.Fatalf("%+v %q", r, f.got)
	}
	f = &fake{out: "nope\n", err: exitErr(3)}
	if r := Run(context.Background(), f, resolve.Options{}, "oak", "false", time.Second); r.OK() || r.Exit != 3 || r.Err != nil {
		t.Fatalf("the command failed, not ssh: %+v", r)
	}
	f = &fake{out: "debug\nssh: connect to host oak port 22: Connection refused\n", err: exitErr(255)}
	if r := Run(context.Background(), f, resolve.Options{}, "oak", "true", time.Second); r.Exit != -1 || !strings.Contains(r.Err.Error(), "Connection refused") {
		t.Fatalf("ssh failed: %+v", r)
	}
	f = &fake{err: errors.New("boom")}
	if r := Run(context.Background(), f, resolve.Options{}, "oak", "true", time.Second); r.Exit != -1 || r.Err.Error() != "boom" {
		t.Fatalf("%+v", r)
	}
}

func TestParseFacts(t *testing.T) {
	f := ParseFacts("kernel=Linux 6.1.0\narch=aarch64\nhostname=oak\nos=Debian GNU/Linux 12 (bookworm)\ncpus=4\nuptime=93784.5\n" +
		"uptime=93784\nload=0.10 0.20 0.30\nmem=512000 4000000\ndisk=10000000 30000000\nupdates=7\nreboot=yes\nnoise\n")
	if f.OS != "Debian GNU/Linux 12 (bookworm)" || f.CPUs != 4 || f.Uptime != 93784*time.Second || f.Load != "0.10 0.20 0.30" ||
		f.MemUsed != 512000 || f.MemTotal != 4000000 || f.DiskTotal != 30000000 || f.Updates != 7 || !f.Reboot || f.Windows {
		t.Fatalf("%+v", f)
	}
	if w := ParseFacts("windows=yes\r\nos=Microsoft Windows 11 Pro\r\nupdates=x\r\n"); !w.Windows || w.OS != "Microsoft Windows 11 Pro" || w.Updates != -1 {
		t.Fatalf("%+v", w)
	}
}

func TestFactsCommand(t *testing.T) {
	if strings.Contains(unixFacts, "'") {
		t.Fatal("the script is passed in single quotes")
	}
	if !strings.HasPrefix(FactsCommand(true), "powershell -NoProfile") {
		t.Fatal(FactsCommand(true))
	}
}

// The script really runs under sh (Git's on Windows).
func TestUnixFactsRuns(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	out, err := exec.Command(sh, "-c", unixFacts).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	f := ParseFacts(string(out))
	if f.Kernel == "" || f.Hostname == "" || f.Arch == "" {
		t.Fatalf("%+v\n%s", f, out)
	}
}

func TestCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "facts.json")
	c := LoadCache(path)
	if err := c.Put("Oak", Facts{OS: "Debian", Updates: 3}); err != nil {
		t.Fatal(err)
	}
	c = LoadCache(path)
	if f, ok := c.Get("oak"); !ok || f.OS != "Debian" {
		t.Fatalf("%+v", f)
	}
	c.Rename("oak", "pi5")
	if _, ok := LoadCache(path).Get("pi5"); !ok {
		t.Fatal("renamed")
	}
}

// The whole command, as a login shell receives it from ssh.
func TestFactsCommandThroughShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	out, err := exec.Command(sh, "-c", FactsCommand(false)).CombinedOutput()
	if f := ParseFacts(string(out)); err != nil || f.Kernel == "" {
		t.Fatalf("%v %+v\n%s", err, f, out)
	}
}

func TestWindowsFactsRuns(t *testing.T) {
	ps, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("no PowerShell")
	}
	args := strings.Fields(FactsCommand(true))[1:]
	out, err := exec.Command(ps, args...).CombinedOutput()
	f := ParseFacts(string(out))
	if err != nil || !f.Windows || f.OS == "" || f.MemTotal == 0 || f.DiskTotal == 0 || f.Uptime == 0 {
		t.Fatalf("%v %+v\n%s", err, f, out)
	}
}

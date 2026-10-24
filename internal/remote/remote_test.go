package remote

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestParseFactsDrives(t *testing.T) {
	f := ParseFacts("disk=1 2\ndrive=40000 100000 /\r\ndrive=1500000 2000000 /mnt/media files\ndrive=5 0 /empty\n" +
		"drive=garbage\ndrive=10 20 D: (Games)\n")
	want := []Disk{{"/", 40000, 100000}, {"/mnt/media files", 1500000, 2000000}, {"D: (Games)", 10, 20}}
	if !reflect.DeepEqual(f.Disks, want) || f.DiskTotal != 2 {
		t.Fatalf("%+v", f.Disks)
	}
	var many strings.Builder
	for i := range maxDisks + 5 {
		fmt.Fprintf(&many, "drive=1 2 /d%d\n", i)
	}
	if n := len(ParseFacts(many.String()).Disks); n != maxDisks {
		t.Fatalf("%d drives kept", n)
	}
}

// factsWith runs the Unix script with fake commands, by name. They're
// shell functions, which win over anything on PATH: Git for Windows' sh
// puts its own /usr/bin first.
func factsWith(t *testing.T, fakes map[string]string) Facts {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	var script strings.Builder
	for name, body := range fakes {
		fmt.Fprintf(&script, "%s() {\n%s\n}\n", name, body)
	}
	script.WriteString(unixFacts)
	out, err := exec.Command(sh, "-c", script.String()).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return ParseFacts(string(out))
}

// The script lists real drives from df and leaves out pseudo filesystems,
// boot and system volumes, and a device mounted twice.
func TestUnixFactsDrives(t *testing.T) {
	f := factsWith(t, map[string]string{"df": `cat <<END
Filesystem     1024-blocks    Used Available Capacity Mounted on
/dev/nvme0n1p2      100000   40000     60000      40% /
tmpfs                 8000       0      8000       0% /run
/dev/nvme0n1p1         500     100       400      20% /boot/efi
/dev/nvme0n1p2      100000   40000     60000      40% /home
/dev/sda1          2000000 1500000    500000      75% /mnt/media files
/dev/loop3           50000   50000         0     100% /snap/core/1
overlay             100000   40000     60000      40% /var/lib/docker/overlay2/x/merged
tank/data           900000  300000    600000      34% /tank/data
END
`})
	want := []Disk{{"/", 40000, 100000}, {"/mnt/media files", 1500000, 2000000}, {"/tank/data", 300000, 900000}}
	if !reflect.DeepEqual(f.Disks, want) {
		t.Fatalf("drives %+v, want %+v", f.Disks, want)
	}
}

// On macOS, disk images are left out and an APFS container counts once,
// as its size less its free space.
func TestUnixFactsMacDrives(t *testing.T) {
	f := factsWith(t, map[string]string{
		"uname":   `echo Darwin`,
		"hdiutil": `printf "framework : 1\n/dev/disk7          \tGUID_partition_scheme\n/dev/disk7s1\t41504653\t/Library/Developer/CoreSimulator/Volumes/iOS_1\n/dev/disk9s1\t41504653\t/Volumes/Some App\n"`,
		"df": `cat <<END
Filesystem     1024-blocks      Used Available Capacity  Mounted on
/dev/disk3s1s1   500000000  12000000  40000000    24%    /
/dev/disk3s6     500000000   2000000  40000000     5%    /System/Volumes/VM
/dev/disk1s2        512000      6000    490000     2%    /System/Volumes/xarts
/dev/disk3s5     500000000 440000000  40000000    92%    /System/Volumes/Data
map auto_home            0         0         0   100%    /System/Volumes/Data/home
/dev/disk7s1      17000000  16500000    500000    98%    /Library/Developer/CoreSimulator/Volumes/iOS_1
/dev/disk9s1         90000     70000     20000    78%    /Volumes/Some App
/dev/disk12s2   2000000000 500000000 1500000000   25%    /Volumes/Photo Archive
END
`})
	want := []Disk{{"/", 460000000, 500000000}, {"/Volumes/Photo Archive", 500000000, 2000000000}}
	if !reflect.DeepEqual(f.Disks, want) {
		t.Fatalf("drives %+v, want %+v", f.Disks, want)
	}
	if f.DiskUsed != 460000000 {
		t.Fatalf("disk used %d: the container's, not the sealed volume's", f.DiskUsed)
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

// The Windows facts command, run by Windows PowerShell. PowerShell on
// Linux or macOS lacks the Windows cmdlets it uses, so it's only run on
// Windows.
func TestWindowsFactsRuns(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs Windows")
	}
	ps, err := exec.LookPath("powershell")
	if err != nil {
		t.Skip("no PowerShell")
	}
	args := strings.Fields(FactsCommand(true))[1:]
	out, err := exec.Command(ps, args...).CombinedOutput()
	f := ParseFacts(string(out))
	if err != nil || !f.Windows || f.OS == "" || f.MemTotal == 0 || f.DiskTotal == 0 || len(f.Disks) == 0 || f.Uptime == 0 {
		t.Fatalf("%v %+v\n%s", err, f, out)
	}
}

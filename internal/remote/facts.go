package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bferg314/quick-ssh/internal/keys"
	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/run"
)

// Facts describe a host, as of At.
type Facts struct {
	At       time.Time     `json:"at"`
	OS       string        `json:"os,omitempty"`     // "Debian GNU/Linux 12 (bookworm)"
	Kernel   string        `json:"kernel,omitempty"` // "Linux 6.1.0-18-arm64"
	Arch     string        `json:"arch,omitempty"`
	Hostname string        `json:"hostname,omitempty"`
	CPUs     int           `json:"cpus,omitempty"`
	Uptime   time.Duration `json:"uptime,omitempty"`
	Load     string        `json:"load,omitempty"` // 1, 5 and 15 minute averages
	// Memory and disk (the root or C: drive) in KiB.
	MemUsed   int64 `json:"mem_used,omitempty"`
	MemTotal  int64 `json:"mem_total,omitempty"`
	DiskUsed  int64 `json:"disk_used,omitempty"`
	DiskTotal int64 `json:"disk_total,omitempty"`
	// Updates is the number of pending package updates, -1 when unknown.
	Updates int    `json:"updates"`
	Reboot  bool   `json:"reboot,omitempty"` // a reboot is pending
	Windows bool   `json:"windows,omitempty"`
	Err     string `json:"err,omitempty"`
}

// unixFacts prints key=value lines. It reads only what's available
// without root and without the network (apt-get -s, dnf --cacheonly,
// pacman -Qu). It contains no single quotes, so it can be passed as
// sh -c '...' through any login shell.
const unixFacts = `echo "kernel=$(uname -s) $(uname -r)"
echo "arch=$(uname -m)"
echo "hostname=$(hostname 2>/dev/null || uname -n)"
if [ -r /etc/os-release ]; then . /etc/os-release; echo "os=$PRETTY_NAME"
elif command -v sw_vers >/dev/null 2>&1; then echo "os=$(sw_vers -productName) $(sw_vers -productVersion)"; fi
if command -v nproc >/dev/null 2>&1; then echo "cpus=$(nproc)"; else echo "cpus=$(sysctl -n hw.ncpu 2>/dev/null)"; fi
if [ -r /proc/uptime ]; then read u _ < /proc/uptime; echo "uptime=${u%.*}"
else b=$(sysctl -n kern.boottime 2>/dev/null | sed -n "s/.*sec = \([0-9]*\).*/\1/p"); [ -n "$b" ] && echo "uptime=$(( $(date +%s) - b ))"; fi
if [ -r /proc/loadavg ]; then read l1 l5 l15 _ < /proc/loadavg; echo "load=$l1 $l5 $l15"
else echo "load=$(sysctl -n vm.loadavg 2>/dev/null | tr -d "{}" | awk "{print \$1, \$2, \$3}")"; fi
[ -r /proc/meminfo ] && awk "/^MemTotal:/{t=\$2} /^MemAvailable:/{a=\$2} END{print \"mem=\" t-a \" \" t}" /proc/meminfo
df -Pk / 2>/dev/null | awk "NR==2{print \"disk=\" \$3 \" \" \$2}"
if command -v apt-get >/dev/null 2>&1; then echo "updates=$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep -c ^Inst)"
elif command -v dnf >/dev/null 2>&1; then echo "updates=$(dnf -q --cacheonly check-update 2>/dev/null | grep -c "^[A-Za-z0-9]")"
elif command -v pacman >/dev/null 2>&1; then echo "updates=$(pacman -Qu 2>/dev/null | wc -l)"; fi
[ -e /var/run/reboot-required ] && echo "reboot=yes"
true`

const windowsFacts = `$ErrorActionPreference = 'SilentlyContinue'
$o = Get-CimInstance Win32_OperatingSystem
"windows=yes"
"os=" + $o.Caption
"kernel=Windows " + $o.Version
"arch=" + $env:PROCESSOR_ARCHITECTURE
"hostname=" + $env:COMPUTERNAME
"cpus=" + $env:NUMBER_OF_PROCESSORS
"uptime=" + [int64]((Get-Date) - $o.LastBootUpTime).TotalSeconds
"mem=" + ($o.TotalVisibleMemorySize - $o.FreePhysicalMemory) + " " + $o.TotalVisibleMemorySize
$d = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='C:'"
"disk=" + [int64](($d.Size - $d.FreeSpace) / 1024) + " " + [int64]($d.Size / 1024)
if (Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired') { "reboot=yes" }
`

// FactsCommand is the remote command that prints a host's facts.
func FactsCommand(windows bool) string {
	if windows {
		return "powershell -NoProfile -NonInteractive -EncodedCommand " + keys.EncodePowerShell(windowsFacts)
	}
	return "sh -c '" + unixFacts + "'"
}

// GetFacts fetches alias's facts. windows picks the PowerShell script.
func GetFacts(ctx context.Context, r run.Runner, o resolve.Options, alias string, windows bool) Facts {
	res := Run(ctx, r, o, alias, FactsCommand(windows), 30*time.Second)
	f := ParseFacts(res.Output)
	f.At = time.Now()
	if res.Err != nil {
		f.Err = res.Err.Error()
	} else if f.Kernel == "" && f.OS == "" {
		f.Err = "the host printed nothing quick-ssh understands"
		if l := lastLine(res.Output); l != "" {
			f.Err += ": " + l
		}
	}
	return f
}

// ParseFacts reads the scripts' key=value lines.
func ParseFacts(out string) Facts {
	f := Facts{Updates: -1}
	two := func(v string) (int64, int64) {
		a, b, _ := strings.Cut(strings.TrimSpace(v), " ")
		x, _ := strconv.ParseInt(a, 10, 64)
		y, _ := strconv.ParseInt(b, 10, 64)
		return x, y
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimRight(sc.Text(), "\r"), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "os":
			f.OS = strings.Trim(v, `"`)
		case "kernel":
			f.Kernel = v
		case "arch":
			f.Arch = v
		case "hostname":
			f.Hostname = v
		case "cpus":
			f.CPUs, _ = strconv.Atoi(v)
		case "uptime":
			if s, err := strconv.ParseInt(v, 10, 64); err == nil {
				f.Uptime = time.Duration(s) * time.Second
			}
		case "load":
			f.Load = v
		case "mem":
			f.MemUsed, f.MemTotal = two(v)
		case "disk":
			f.DiskUsed, f.DiskTotal = two(v)
		case "updates":
			if n, err := strconv.Atoi(v); err == nil {
				f.Updates = n
			}
		case "reboot":
			f.Reboot = v == "yes"
		case "windows":
			f.Windows = v == "yes"
		}
	}
	return f
}

// Cache keeps the last facts fetched for each host, in facts.json in the
// OS cache folder, so the detail pane has them the next time.
type Cache struct {
	mu    sync.Mutex
	path  string
	Hosts map[string]Facts `json:"hosts"` // by lowercase alias
}

// DefaultCachePath is facts.json in quick-ssh's cache folder.
func DefaultCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "quick-ssh", "facts.json")
}

// LoadCache reads the cache; a missing or broken file is an empty cache.
func LoadCache(path string) *Cache {
	c := &Cache{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, c)
	}
	if c.Hosts == nil {
		c.Hosts = map[string]Facts{}
	}
	return c
}

// Get returns alias's facts, if any were fetched.
func (c *Cache) Get(alias string) (Facts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.Hosts[strings.ToLower(alias)]
	return f, ok
}

// Put records alias's facts and saves the cache.
func (c *Cache) Put(alias string, f Facts) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Hosts[strings.ToLower(alias)] = f
	if c.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Rename moves alias's facts to a new name.
func (c *Cache) Rename(from, to string) error {
	f, ok := c.Get(from)
	if !ok {
		return nil
	}
	c.mu.Lock()
	delete(c.Hosts, strings.ToLower(from))
	c.mu.Unlock()
	return c.Put(to, f)
}

// Path is where the cache is saved.
func (c *Cache) Path() string { return c.path }

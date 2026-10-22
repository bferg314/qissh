package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bferg314/qissh/internal/config"
	"github.com/bferg314/qissh/internal/history"
	"github.com/bferg314/qissh/internal/probe"
	"github.com/bferg314/qissh/internal/remote"
	"github.com/bferg314/qissh/internal/tailscale"
)

// TestScreenshot draws the README's screenshot from a made-up fleet. It
// runs only when QS_SCREENSHOT names the HTML file to write:
//
//	QS_SCREENSHOT=$PWD/docs/screenshot.html go test ./internal/ui -run TestScreenshot
//
// then open that file in a browser and capture the terminal.
func TestScreenshot(t *testing.T) {
	out := os.Getenv("QS_SCREENSHOT")
	if out == "" {
		t.Skip("set QS_SCREENSHOT to write the screenshot")
	}
	const w, h = 140, 30
	now := time.Now()
	dir := t.TempDir()
	// A home of its own, so paths show as ~/.ssh/config.
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	sshPath := filepath.Join(dir, ".ssh", "config")
	os.MkdirAll(filepath.Dir(sshPath), 0o700)
	os.WriteFile(sshPath, []byte(demoSSHConfig), 0o600)

	hist := history.History{Hosts: map[string]*history.Entry{
		"web1":    {Count: 42, Last: now.Add(-2 * time.Hour)},
		"db":      {Count: 17, Last: now.Add(-26 * time.Hour)},
		"nas":     {Count: 9, Last: now.Add(-3 * 24 * time.Hour)},
		"bastion": {Count: 6, Last: now.Add(-9 * 24 * time.Hour)},
		"web2":    {Count: 3, Last: now.Add(-20 * 24 * time.Hour)},
	}}
	writeJSON(t, filepath.Join(dir, "history.json"), hist)
	writeJSON(t, filepath.Join(dir, "facts.json"), remote.Cache{Hosts: map[string]remote.Facts{
		"web1": {At: now.Add(-40 * time.Minute), OS: "Debian GNU/Linux 12 (bookworm)", Kernel: "Linux 6.1.0-28-amd64", Arch: "x86_64",
			CPUs: 4, Uptime: 19*24*time.Hour + 4*time.Hour, Load: "0.42 0.37 0.31",
			MemUsed: 2_900 << 10, MemTotal: 8_192 << 10, DiskUsed: 31 << 20, DiskTotal: 80 << 20, Updates: 3}, // KiB
	}})

	cfg := config.Default()
	cfg.SSHConfig = sshPath
	cfg.Tools = nil
	for alias, tags := range map[string][]string{"web1": {"prod"}, "web2": {"prod"}, "db": {"prod"}, "nas": {"home"}, "garage-pi": {"home", "pi"}} {
		cfg.SetMeta(alias, config.HostMeta{Tags: tags})
	}
	a := New(Options{Config: cfg, ConfigPath: filepath.Join(dir, "config.toml"), Runner: demoRunner{},
		History: history.Load(filepath.Join(dir, "history.json")), BackupDir: filepath.Join(dir, "backups"),
		KeyDir: filepath.Join(dir, "keys"), FactsPath: filepath.Join(dir, "facts.json")})
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	resolveSync(a)
	a.Update(tailscaleMsg{peers: []*tailscale.Peer{
		{DNSName: "nas.example.ts.net.", OS: "linux", Online: true},
		{DNSName: "garage-pi.example.ts.net.", OS: "linux", LastSeen: now.Add(-3 * 24 * time.Hour)},
	}})
	for alias, r := range map[string]probe.Reach{
		"web1":    {Up: true, Latency: 23 * time.Millisecond},
		"bastion": {Up: true, Latency: 18 * time.Millisecond},
		"nas":     {Up: true, Latency: 4 * time.Millisecond},
		"web2":    {Err: "timeout"},
	} {
		r.At = now
		a.checks.reach[config.HostKey(alias)] = r
	}
	a.refresh()
	a.hosts.focus("web1")
	a.toast = toast{}

	screen := strings.ReplaceAll(a.render(), `~\.ssh\config`, "~/.ssh/config") // as on Linux and macOS
	page := fmt.Sprintf(screenshotPage, ansiHTML(screen))
	if err := os.WriteFile(out, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
}

const demoSSHConfig = `Host bastion
    HostName bastion.example.com
    User admin

Host web1
    HostName web1.example.com
    User deploy

Host web2
    HostName web2.example.com
    User deploy

Host db
    HostName db.internal
    User deploy
    Port 2222
    ProxyJump bastion

Host nas
    HostName nas.example.ts.net
    User admin

Host garage-pi
    HostName garage-pi.example.ts.net
    User pi

Host *
    ServerAliveInterval 30
`

// demoRunner answers ssh -G for the demo fleet.
type demoRunner struct{}

func (demoRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "ssh" {
		return nil, fmt.Errorf("%s isn't here", name)
	}
	var host, user, port, jump string
	alias := args[len(args)-1]
	block := false
	for _, l := range strings.Split(demoSSHConfig, "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) < 2:
		case f[0] == "Host":
			block = f[1] == alias
		case block && f[0] == "HostName":
			host = f[1]
		case block && f[0] == "User":
			user = f[1]
		case block && f[0] == "Port":
			port = f[1]
		case block && f[0] == "ProxyJump":
			jump = f[1]
		}
	}
	if port == "" {
		port = "22"
	}
	s := fmt.Sprintf("hostname %s\nuser %s\nport %s\nserveraliveinterval 30\n", host, user, port)
	if jump != "" {
		s += "proxyjump " + jump + "\n"
	}
	return []byte(s), nil
}

func (demoRunner) Combined(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// ansiHTML turns the rendered screen's SGR colours into HTML spans.
func ansiHTML(s string) string {
	var b strings.Builder
	fg, bg := "", ""
	var bold, faint, reverse bool
	open := false
	flush := func() {
		if open {
			b.WriteString("</span>")
			open = false
		}
		f, g := fg, bg
		if reverse {
			f, g = bg, fg
			if f == "" {
				f = "var(--bg)"
			}
			if g == "" {
				g = "var(--fg)"
			}
		}
		var css []string
		if f != "" {
			css = append(css, "color:"+f)
		}
		if g != "" {
			css = append(css, "background:"+g)
		}
		if bold {
			css = append(css, "font-weight:bold")
		}
		if faint {
			css = append(css, "opacity:.6")
		}
		if len(css) > 0 {
			b.WriteString(`<span style="` + strings.Join(css, ";") + `">`)
			open = true
		}
	}
	for i := 0; i < len(s); {
		if s[i] != '\x1b' {
			j := strings.IndexByte(s[i:], '\x1b')
			if j < 0 {
				j = len(s) - i
			}
			b.WriteString(html.EscapeString(s[i : i+j]))
			i += j
			continue
		}
		// ESC [ params m; anything else is skipped.
		end := i + 2
		for end < len(s) && (s[end] < 0x40 || s[end] > 0x7e) {
			end++
		}
		if i+1 < len(s) && s[i+1] == '[' && end < len(s) && s[end] == 'm' {
			ps := strings.Split(s[i+2:end], ";")
			for k := 0; k < len(ps); k++ {
				n, _ := strconv.Atoi(ps[k])
				switch {
				case n == 0:
					fg, bg, bold, faint, reverse = "", "", false, false, false
				case n == 1:
					bold = true
				case n == 2:
					faint = true
				case n == 22:
					bold, faint = false, false
				case n == 7:
					reverse = true
				case n == 27:
					reverse = false
				case n == 39:
					fg = ""
				case n == 49:
					bg = ""
				case (n == 38 || n == 48) && k+4 < len(ps) && ps[k+1] == "2":
					c := fmt.Sprintf("rgb(%s,%s,%s)", ps[k+2], ps[k+3], ps[k+4])
					if n == 38 {
						fg = c
					} else {
						bg = c
					}
					k += 4
				case (n == 38 || n == 48) && k+2 < len(ps) && ps[k+1] == "5":
					c := ansi256(ps[k+2])
					if n == 38 {
						fg = c
					} else {
						bg = c
					}
					k += 2
				case n >= 30 && n <= 37, n >= 90 && n <= 97:
					fg = ansi256(strconv.Itoa(basicIndex(n)))
				case n >= 40 && n <= 47, n >= 100 && n <= 107:
					bg = ansi256(strconv.Itoa(basicIndex(n - 10)))
				}
			}
			flush()
		}
		i = end + 1
	}
	if open {
		b.WriteString("</span>")
	}
	return b.String()
}

func basicIndex(n int) int {
	if n >= 90 {
		return n - 90 + 8
	}
	return n - 30
}

// ansi256 is xterm's colour n.
func ansi256(s string) string {
	n, _ := strconv.Atoi(s)
	base := []string{"#1d1f21", "#cc6666", "#b5bd68", "#f0c674", "#81a2be", "#b294bb", "#8abeb7", "#c5c8c6",
		"#666666", "#d54e53", "#b9ca4a", "#e7c547", "#7aa6da", "#c397d8", "#70c0b1", "#eaeaea"}
	switch {
	case n < 16:
		return base[n]
	case n < 232:
		n -= 16
		lv := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return fmt.Sprintf("rgb(%d,%d,%d)", lv(n/36), lv(n/6%6), lv(n%6))
	default:
		v := 8 + (n-232)*10
		return fmt.Sprintf("rgb(%d,%d,%d)", v, v, v)
	}
}

const screenshotPage = `<!doctype html>
<meta charset="utf-8">
<title>qissh</title>
<style>
  :root { --bg: #16161e; --fg: #c8ccd4; }
  html, body { margin: 0; background: #0d1117; }
  .term { display: inline-block; margin: 24px; border-radius: 10px; overflow: hidden;
          box-shadow: 0 12px 40px rgba(0,0,0,.5); background: var(--bg); }
  .bar { height: 30px; background: #22232e; display: flex; align-items: center; gap: 8px; padding: 0 14px; }
  .bar i { width: 12px; height: 12px; border-radius: 50%%; display: inline-block; }
  pre { margin: 0; padding: 14px 16px 16px; color: var(--fg);
        font: 15px/1.2 "Cascadia Mono", "JetBrains Mono", Consolas, monospace; }
</style>
<div class="term">
  <div class="bar"><i style="background:#ff5f57"></i><i style="background:#febc2e"></i><i style="background:#28c840"></i></div>
<pre>%s</pre>
</div>
`

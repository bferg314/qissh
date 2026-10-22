// Package launcher builds the commands qissh starts: ssh sessions,
// tools, editors, and the terminal tabs they can open in.
package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/bferg314/qissh/internal/config"
)

// Vars are what a tool's arguments can refer to.
type Vars struct {
	Alias, HostName, User string
	Port                  int
}

// Home is the user's likely home folder on the host: /root for root,
// otherwise /home/<user>.
func (v Vars) Home() string {
	switch v.User {
	case "":
		return "~"
	case "root":
		return "/root"
	}
	return "/home/" + v.User
}

// Expand replaces {alias}, {hostname}, {user}, {port}, {home} and {path}
// in s.
func (v Vars) Expand(s string) string {
	port := ""
	if v.Port != 0 {
		port = strconv.Itoa(v.Port)
	}
	return strings.NewReplacer(
		"{alias}", v.Alias,
		"{hostname}", v.HostName,
		"{user}", v.User,
		"{port}", port,
		"{home}", v.Home(),
	).Replace(s)
}

// Command builds the exec.Cmd for tool t, expanding its arguments.
func Command(t config.Tool, v Vars) *exec.Cmd {
	args := make([]string, len(t.Args))
	for i, a := range t.Args {
		args[i] = v.Expand(a)
	}
	return exec.Command(t.Cmd, args...)
}

// InZellij reports whether qissh is running inside a zellij session.
func InZellij() bool { return os.Getenv("ZELLIJ") != "" }

// InWindowsTerminal reports whether qissh is running inside Windows
// Terminal.
func InWindowsTerminal() bool {
	return runtime.GOOS == "windows" && os.Getenv("WT_SESSION") != ""
}

// ZellijTab builds a command that opens a new zellij tab named name running
// argv. It returns once the tab is open.
func ZellijTab(name string, argv []string) *exec.Cmd {
	args := []string{"action", "new-tab", "--name", name, "--close-on-exit", "--"}
	return exec.Command("zellij", append(args, resolve(argv)...)...)
}

// WTTab builds a command that opens a new tab titled name in the current
// Windows Terminal window, running argv.
func WTTab(name string, argv []string) *exec.Cmd {
	args := []string{"-w", "0", "new-tab", "--title", name}
	return exec.Command("wt", append(args, resolve(argv)...)...)
}

// resolve replaces argv[0] with the path found on PATH, so the new tab
// runs what qissh found rather than whatever its shell finds.
func resolve(argv []string) []string {
	out := append([]string{}, argv...)
	if p, err := exec.LookPath(out[0]); err == nil {
		out[0] = p
	}
	return out
}

// Detach starts cmd without waiting and without tying it to our terminal.
func Detach(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// OpenURL opens url in the default browser.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// Avoids cmd.exe's "start", which mangles URLs containing "&".
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return Detach(cmd)
}

// Editor is the editor for the ssh config: $VISUAL or $EDITOR (which may
// carry arguments, such as "code --wait"), else vim, nvim, nano or vi,
// else Notepad on Windows. It opens path at line (1-based) when the
// editor has a way to.
func Editor(path string, line int) config.Tool {
	var fields []string
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if fields = strings.Fields(os.Getenv(env)); len(fields) > 0 {
			break
		}
	}
	if len(fields) == 0 {
		for _, e := range []string{"vim", "nvim", "nano", "vi"} {
			if config.Available(e) {
				fields = []string{e}
				break
			}
		}
	}
	if len(fields) == 0 {
		if runtime.GOOS == "windows" {
			return config.Tool{Name: "Notepad", Cmd: "notepad", Args: []string{path}, Mode: config.ModeDetach}
		}
		fields = []string{"vi"}
	}

	t := config.Tool{Name: filepath.Base(fields[0]), Cmd: fields[0], Args: fields[1:], Mode: config.ModeTerminal}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(fields[0])), ".exe")
	at := strconv.Itoa(max(1, line))
	switch name {
	case "vim", "nvim", "vi", "nano", "emacs", "micro", "kak", "gvim":
		t.Args = append(t.Args, "+"+at, path)
	case "code", "code-insiders", "codium", "cursor", "windsurf":
		t.Args = append(t.Args, "--goto", path+":"+at)
		if !contains(t.Args, "--wait") && !contains(t.Args, "-w") {
			t.Mode = config.ModeDetach
		}
	case "hx", "helix", "subl", "zed":
		t.Args = append(t.Args, path+":"+at)
	default:
		t.Args = append(t.Args, path)
	}
	if name == "notepad" || name == "notepad++" {
		t.Mode = config.ModeDetach
	}
	return t
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

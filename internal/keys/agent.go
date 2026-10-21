package keys

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bferg314/quick-ssh/internal/run"
)

// Agent is what ssh-agent holds, as `ssh-add -l` lists it.
type Agent struct {
	Running bool
	// Fingerprints of the loaded keys.
	Fingerprints map[string]bool
	// Err is why the agent couldn't be reached.
	Err string
}

// Has reports whether the agent holds k.
func (a Agent) Has(k *Key) bool { return k.Fingerprint != "" && a.Fingerprints[k.Fingerprint] }

// LoadAgent asks sshAdd (ssh-add) for the loaded keys.
func LoadAgent(ctx context.Context, r run.Runner, sshAdd string) Agent {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := r.Combined(ctx, sshAdd, "-l")
	text := string(out)
	a := Agent{Fingerprints: map[string]bool{}}
	switch {
	case strings.Contains(text, "has no identities"):
		a.Running = true
	case err != nil:
		a.Err = run.FirstLine(text)
		if a.Err == "" {
			a.Err = err.Error()
		}
	default:
		a.Running = true
		// "256 SHA256:abc… alice@elm (ED25519)"
		for _, line := range strings.Split(text, "\n") {
			if f := strings.Fields(line); len(f) >= 2 && strings.HasPrefix(f[1], "SHA256:") {
				a.Fingerprints[f[1]] = true
			}
		}
	}
	return a
}

// AgentHint explains how to get an agent running when there isn't one.
// On Windows it tells the built-in agent service apart from Git's ssh,
// which can't use that service.
func AgentHint(ctx context.Context, r run.Runner, sshAdd string) string {
	if runtime.GOOS != "windows" {
		if os.Getenv("SSH_AUTH_SOCK") == "" {
			return `No agent is running (SSH_AUTH_SOCK isn't set). Start one with: eval "$(ssh-agent)"`
		}
		return "The agent at $SSH_AUTH_SOCK isn't answering."
	}
	if !windowsOpenSSH(sshAdd) {
		return `No agent: SSH_AUTH_SOCK isn't set. The ssh on your PATH (` + lookPath(sshAdd) + `) isn't Windows' own, so it can't use the ssh-agent service. Run eval "$(ssh-agent)" in Git Bash, or put C:\Windows\System32\OpenSSH first on PATH to use the service.`
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := r.Output(ctx, "sc.exe", "qc", "ssh-agent")
	if strings.Contains(string(out), "DISABLED") {
		return "The ssh-agent service is disabled. In an admin PowerShell: Set-Service ssh-agent -StartupType Automatic; Start-Service ssh-agent"
	}
	return "The ssh-agent service isn't running. In an admin PowerShell: Start-Service ssh-agent"
}

func lookPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// windowsOpenSSH reports whether prog is Windows' bundled OpenSSH, which
// talks to the ssh-agent service rather than SSH_AUTH_SOCK.
func windowsOpenSSH(prog string) bool {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return strings.HasPrefix(strings.ToLower(filepath.Clean(lookPath(prog))), strings.ToLower(filepath.Join(root, "System32", "OpenSSH"))+`\`)
}

// samePath compares paths the way the file system does: ignoring case on
// Windows and macOS.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(filepath.FromSlash(a)), filepath.Clean(filepath.FromSlash(b))
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

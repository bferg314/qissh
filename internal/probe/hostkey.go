package probe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bferg314/quick-ssh/internal/config"
	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/run"
)

// HostKey is what's needed to fix a changed or unknown host key: the key
// the host presents now, what known_hosts holds, and where.
type HostKey struct {
	Alias string
	// Name is how known_hosts refers to the host: its HostKeyAlias or
	// hostname, as [name]:port when the port isn't 22.
	Name       string
	KnownHosts string
	Old        []string // fingerprints known_hosts has for Name
	New        []string // fingerprints the host presents
	newLines   []string // the known_hosts lines ssh wrote for them
}

var fingerprint = regexp.MustCompile(`SHA256:[A-Za-z0-9+/=]+`)

// knownHostsName is how ssh looks the host up in known_hosts.
func knownHostsName(res *resolve.Resolved) string {
	name := strings.ToLower(res.HostName)
	if a := res.Options["hostkeyalias"]; len(a) > 0 && a[0] != "none" {
		name = a[0]
	}
	if res.Port != 0 && res.Port != 22 {
		return "[" + name + "]:" + strconv.Itoa(res.Port)
	}
	return name
}

// KnownHostsFile is the first of the host's UserKnownHostsFile entries, as
// a path this program can open: ssh from Git for Windows reports
// /c/Users/... paths.
func KnownHostsFile(res *resolve.Resolved) string {
	files := res.Options["userknownhostsfile"]
	if len(files) == 0 {
		return config.ExpandPath("~/.ssh/known_hosts")
	}
	first := strings.Fields(files[0])[0]
	if runtime.GOOS == "windows" && len(first) >= 3 && first[0] == '/' && first[2] == '/' {
		first = strings.ToUpper(first[1:2]) + ":" + first[2:]
	}
	return config.ExpandPath(first)
}

// ScanHostKey connects to alias with a throwaway known_hosts file that
// accepts whatever key the host presents, so the key can be shown before
// anyone trusts it. It goes through ProxyJump like a real connection and
// writes nothing to the real known_hosts.
func ScanHostKey(ctx context.Context, r run.Runner, o resolve.Options, alias string, res *resolve.Resolved) (*HostKey, error) {
	hk := &HostKey{Alias: alias, Name: knownHostsName(res), KnownHosts: KnownHostsFile(res)}

	dir, err := os.MkdirTemp("", "quick-ssh-hostkey-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	scanned, empty := filepath.Join(dir, "known_hosts"), filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	opts := append(append([]string{}, batch...),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile="+filepath.ToSlash(scanned),
		"-o", "GlobalKnownHostsFile="+filepath.ToSlash(empty))
	out, _ := r.Combined(ctx, o.Program(), append(o.ArgsWith(opts, alias), "exit 0")...)

	data, err := os.ReadFile(scanned)
	if err != nil || strings.TrimSpace(string(data)) == "" {
		if line := explanation(string(out)); line != "" {
			return nil, fmt.Errorf("couldn't get the host key: %s", line)
		}
		return nil, fmt.Errorf("couldn't get the host key")
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			hk.newLines = append(hk.newLines, l)
		}
	}
	keygen := keygenFor(o)
	if fp, err := r.Output(ctx, keygen, "-l", "-f", scanned); err == nil {
		hk.New = fingerprint.FindAllString(string(fp), -1)
	}
	if fp, err := r.Output(ctx, keygen, "-l", "-F", hk.Name, "-f", hk.KnownHosts); err == nil {
		hk.Old = fingerprint.FindAllString(string(fp), -1)
	}
	return hk, nil
}

// Trust replaces whatever known_hosts has for the host with the keys
// ScanHostKey saw. ssh-keygen -R keeps the old file as known_hosts.old.
func Trust(ctx context.Context, r run.Runner, o resolve.Options, hk *HostKey) error {
	if len(hk.newLines) == 0 {
		return fmt.Errorf("no host key to trust")
	}
	if len(hk.Old) > 0 {
		if _, err := r.Output(ctx, keygenFor(o), "-R", hk.Name, "-f", hk.KnownHosts); err != nil {
			return fmt.Errorf("removing the old key: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(hk.KnownHosts), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(hk.KnownHosts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	prefix := ""
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 && !endsWithNewline(hk.KnownHosts) {
		prefix = "\n"
	}
	_, werr := f.WriteString(prefix + strings.Join(hk.newLines, "\n") + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func endsWithNewline(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && (len(data) == 0 || data[len(data)-1] == '\n')
}

// keygenFor is the ssh-keygen that goes with the ssh being used: beside
// it when ssh is a path, otherwise whichever is on PATH.
func keygenFor(o resolve.Options) string {
	p := o.Program()
	if strings.ContainsAny(p, `/\`) {
		ext := filepath.Ext(p)
		return filepath.Join(filepath.Dir(p), "ssh-keygen"+ext)
	}
	return "ssh-keygen"
}

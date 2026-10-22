// Package resolve asks ssh what it will actually use for a host. `ssh -G`
// applies Host * defaults, Match blocks, Includes and first-match-wins
// ordering, so qissh never has to reimplement those rules.
package resolve

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bferg314/qissh/internal/run"
)

// Resolved is a host's effective configuration.
type Resolved struct {
	HostName      string
	User          string
	Port          int
	IdentityFiles []string
	ProxyJump     string
	ProxyCommand  string
	// Options holds every keyword ssh printed (lowercase), in order of
	// appearance for repeated ones such as identityfile and localforward.
	Options map[string][]string
	Err     error
}

// Target is user@hostname, with :port when it isn't 22.
func (r *Resolved) Target() string {
	t := r.HostName
	if r.User != "" {
		t = r.User + "@" + t
	}
	if r.Port != 0 && r.Port != 22 {
		t += ":" + strconv.Itoa(r.Port)
	}
	return t
}

// Jump is how the host is reached when not directly: the ProxyJump hosts,
// or "ProxyCommand".
func (r *Resolved) Jump() string {
	switch {
	case r.ProxyJump != "":
		return r.ProxyJump
	case r.ProxyCommand != "":
		return "ProxyCommand"
	}
	return ""
}

// Options for running ssh.
type Options struct {
	SSH        string // the ssh program; "" means "ssh"
	ConfigFile string // passed as -F when set
	Timeout    time.Duration
}

// Program is the ssh to run.
func (o Options) Program() string {
	if o.SSH == "" {
		return "ssh"
	}
	return o.SSH
}

// Tool is one of ssh's companion programs (ssh-keygen, ssh-add) that goes
// with the ssh being used: beside it when ssh is a path, otherwise
// whichever is on PATH.
func (o Options) Tool(name string) string {
	p := o.Program()
	if strings.ContainsAny(p, `/\`) {
		return filepath.Join(filepath.Dir(p), name+filepath.Ext(p))
	}
	return name
}

// Args are the arguments that put ssh in front of alias: -F when a config
// file is set, then the alias.
func (o Options) Args(alias string) []string { return o.ArgsWith(nil, alias) }

// ArgsWith puts -F (when set), then extra options such as -o BatchMode=yes,
// before alias.
func (o Options) ArgsWith(extra []string, alias string) []string {
	var args []string
	if o.ConfigFile != "" {
		args = append(args, "-F", o.ConfigFile)
	}
	args = append(args, extra...)
	if strings.HasPrefix(alias, "-") {
		args = append(args, "--") // so the alias isn't read as an option
	}
	return append(args, alias)
}

// Validator checks a config file before qissh puts it in place, by asking
// `ssh -F <file> -G <probe>` to read it. ssh's complaint comes back
// starting at "line N:", without the temp file's name.
func Validator(r run.Runner, o Options, probe string) func(path string) error {
	return func(path string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := r.Output(ctx, o.Program(), "-F", path, "-G", probe)
		if err != nil {
			// "C:\...\.config.qs-123: line 9: Bad configuration option: x" → "line 9: ..."
			msg := err.Error()
			if i := strings.Index(msg, ": line "); i >= 0 {
				msg = msg[i+2:]
			}
			return errors.New(msg)
		}
		return nil
	}
}

// One resolves a single host.
func One(ctx context.Context, r run.Runner, o Options, alias string) *Resolved {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	args := append([]string{"-G"}, o.Args(alias)...)
	out, err := r.Output(ctx, o.Program(), args...)
	if err != nil {
		return &Resolved{Err: err}
	}
	return Parse(string(out))
}

// All resolves aliases, at most workers at a time, calling done for each
// as it finishes.
func All(ctx context.Context, r run.Runner, o Options, aliases []string, workers int, done func(alias string, res *Resolved)) {
	sem := make(chan struct{}, max(1, workers))
	var wg sync.WaitGroup
	for _, a := range aliases {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			done(a, One(ctx, r, o, a))
		}()
	}
	wg.Wait()
}

// Parse reads `ssh -G` output: one "keyword value" pair per line.
func Parse(out string) *Resolved {
	r := &Resolved{Options: map[string][]string{}}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		k, v, _ := strings.Cut(line, " ")
		if k == "" {
			continue
		}
		k = strings.ToLower(k)
		r.Options[k] = append(r.Options[k], v)
	}
	first := func(k string) string {
		if vs := r.Options[k]; len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	r.HostName = first("hostname")
	r.User = first("user")
	r.Port, _ = strconv.Atoi(first("port"))
	r.IdentityFiles = r.Options["identityfile"]
	if j := first("proxyjump"); j != "none" {
		r.ProxyJump = j
	}
	if p := first("proxycommand"); p != "none" {
		r.ProxyCommand = p
	}
	return r
}

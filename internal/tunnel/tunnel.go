// Package tunnel runs port forwards as background `ssh -N` processes.
package tunnel

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/run"
)

// Spec is a forward: Kind L (local), R (remote) or D (dynamic, a SOCKS
// proxy), and the argument ssh's -L, -R or -D takes.
type Spec struct {
	Kind byte
	Arg  string
}

func (s Spec) String() string { return string(s.Kind) + " " + s.Arg }

// Describe says what the forward does, e.g. "localhost:5432 → localhost:5432 on the host".
func (s Spec) Describe() string {
	parts := strings.Split(s.Arg, ":")
	switch s.Kind {
	case 'D':
		return "SOCKS proxy on localhost:" + parts[len(parts)-1]
	case 'L':
		if len(parts) >= 3 {
			n := len(parts)
			return "localhost:" + parts[n-3] + " → " + parts[n-2] + ":" + parts[n-1] + " from the host"
		}
	case 'R':
		if len(parts) >= 3 {
			n := len(parts)
			return "the host's port " + parts[n-3] + " → " + parts[n-2] + ":" + parts[n-1] + " from here"
		}
	}
	return s.String()
}

// LocalPort is the port the forward listens on here (L and D), or 0.
func (s Spec) LocalPort() int {
	if s.Kind == 'R' {
		return 0
	}
	parts := strings.Split(s.Arg, ":")
	i := 0
	if s.Kind == 'L' && len(parts) >= 3 {
		i = len(parts) - 3
	} else {
		i = len(parts) - 1
	}
	p, _ := strconv.Atoi(parts[i])
	return p
}

// Parse reads a forward the way people write it:
//
//	5432                      L 5432:localhost:5432
//	8080:80                   L 8080:localhost:80
//	8080:db:5432              L 8080:db:5432
//	L 8080:db:5432, -L ...    as written
//	R 9000:localhost:3000     remote forward
//	D 1080                    SOCKS proxy
func Parse(s string) (Spec, error) {
	s = strings.TrimSpace(s)
	kind := byte('L')
	if f := strings.Fields(s); len(f) == 2 {
		k := strings.ToUpper(strings.TrimPrefix(f[0], "-"))
		if len(k) != 1 || !strings.Contains("LRD", k) {
			return Spec{}, fmt.Errorf("%q: start with L, R or D", f[0])
		}
		kind, s = k[0], f[1]
	} else if len(f) != 1 {
		return Spec{}, errors.New("write it like 5432, 8080:localhost:80, R 9000:localhost:3000 or D 1080")
	}
	parts := strings.Split(s, ":")
	port := func(p string) error {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%q isn't a port (1-65535)", p)
		}
		return nil
	}
	for _, p := range parts {
		if p == "" {
			return Spec{}, fmt.Errorf("%q has an empty part", s)
		}
	}
	// The last part is always a port, and so is the listening one: the
	// first, or the second after a bind address.
	if err := port(parts[len(parts)-1]); err != nil {
		return Spec{}, err
	}
	if kind != 'D' && len(parts) <= 4 {
		if err := port(parts[max(0, len(parts)-3)]); err != nil {
			return Spec{}, err
		}
	}
	switch {
	case kind == 'D' && len(parts) <= 2:
	case kind == 'D':
		return Spec{}, errors.New("a SOCKS proxy is D port, or D address:port")
	case len(parts) == 1:
		s = parts[0] + ":localhost:" + parts[0]
	case len(parts) == 2:
		s = parts[0] + ":localhost:" + parts[1]
	case len(parts) == 3 || len(parts) == 4:
	default:
		return Spec{}, fmt.Errorf("%q has too many parts", s)
	}
	return Spec{Kind: kind, Arg: s}, nil
}

// Args is the ssh command line for the forward: no command, no
// terminal, BatchMode so it can't wait at a prompt, keepalives so a dead
// link is noticed, and exit if the forward can't be set up.
func Args(o resolve.Options, alias string, s Spec) []string {
	extra := []string{"-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3", "-o", "ConnectTimeout=10",
		"-" + string(s.Kind), s.Arg}
	return o.ArgsWith(extra, alias)
}

// Proc is a running process.
type Proc interface {
	Wait() error
	Kill() error
}

// Starter starts a program without waiting for it.
type Starter func(name string, args ...string) (Proc, error)

// Exec starts real processes, without a terminal (see run).
func Exec(name string, args ...string) (Proc, error) {
	cmd := exec.Command(name, args...)
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	run.NoTerminal(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProc{cmd: cmd, out: &out}, nil
}

type execProc struct {
	cmd *exec.Cmd
	out *limitedBuffer
}

// Wait returns ssh's last words when it fails, which say why.
func (p *execProc) Wait() error {
	err := p.cmd.Wait()
	if err != nil {
		if l := p.out.lastLine(); l != "" {
			return errors.New(l)
		}
	}
	return err
}

func (p *execProc) Kill() error { return p.cmd.Process.Kill() }

// limitedBuffer keeps the last 4 KiB a process printed.
type limitedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > 4096 {
		b.buf = b.buf[len(b.buf)-4096:]
	}
	return len(p), nil
}

func (b *limitedBuffer) lastLine() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(b.buf)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Tunnel is a forward that's running.
type Tunnel struct {
	Alias   string
	Name    string
	Spec    Spec
	Started time.Time
	proc    Proc
	stopped bool
}

// Key identifies a tunnel: its host and forward.
func Key(alias string, s Spec) string { return strings.ToLower(alias) + " " + s.String() }

// Manager keeps track of running tunnels.
type Manager struct {
	mu      sync.Mutex
	start   Starter
	running map[string]*Tunnel
}

// NewManager uses start to run ssh; nil means Exec.
func NewManager(start Starter) *Manager {
	if start == nil {
		start = Exec
	}
	return &Manager{start: start, running: map[string]*Tunnel{}}
}

// Start runs the forward and calls done (from another goroutine) when
// it ends, with nil if it was stopped on purpose.
func (m *Manager) Start(o resolve.Options, alias, name string, s Spec, done func(err error)) error {
	key := Key(alias, s)
	m.mu.Lock()
	if _, ok := m.running[key]; ok {
		m.mu.Unlock()
		return fmt.Errorf("%s is already running", s)
	}
	m.mu.Unlock()
	p, err := m.start(o.Program(), Args(o, alias, s)...)
	if err != nil {
		return err
	}
	t := &Tunnel{Alias: alias, Name: name, Spec: s, Started: time.Now(), proc: p}
	m.mu.Lock()
	m.running[key] = t
	m.mu.Unlock()
	go func() {
		err := p.Wait()
		m.mu.Lock()
		stopped := t.stopped
		if m.running[key] == t {
			delete(m.running, key)
		}
		m.mu.Unlock()
		if stopped {
			err = nil
		} else if err == nil {
			err = errors.New("ssh exited")
		}
		done(err)
	}()
	return nil
}

// Stop ends the tunnel, if it's running.
func (m *Manager) Stop(alias string, s Spec) bool {
	m.mu.Lock()
	t := m.running[Key(alias, s)]
	if t != nil {
		t.stopped = true
		delete(m.running, Key(alias, s))
	}
	m.mu.Unlock()
	if t != nil {
		_ = t.proc.Kill()
	}
	return t != nil
}

// StopAll ends every tunnel.
func (m *Manager) StopAll() {
	for _, t := range m.List() {
		m.Stop(t.Alias, t.Spec)
	}
}

// Running reports whether the forward is up.
func (m *Manager) Running(alias string, s Spec) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[Key(alias, s)]
	return ok
}

// List is every running tunnel, oldest first.
func (m *Manager) List() []Tunnel {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Tunnel, 0, len(m.running))
	for _, t := range m.running {
		out = append(out, *t)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Started.Before(out[j-1].Started); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

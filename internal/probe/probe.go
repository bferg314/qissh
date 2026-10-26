// Package probe answers "is it up, and can I log in?" for a host, without
// ever stopping at a prompt: reachability is a plain TCP connect, and
// anything that runs ssh uses BatchMode, a ConnectTimeout and no terminal.
package probe

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bferg314/qissh/internal/resolve"
	"github.com/bferg314/qissh/internal/run"
)

// Reach is the result of a TCP connect to a host's ssh port.
type Reach struct {
	Up      bool
	Latency time.Duration
	Err     string // short: "refused", "timeout", "no dns", "slow dns", "unreachable", or ssh's words
	At      time.Time
	// Addr is the address the name resolved to: the one that answered, or
	// else the last one tried. Empty when the lookup failed.
	Addr string
	// Stale is set when the lookup failed and Addr comes from an earlier one.
	Stale bool
}

// lookupTimeout is how long Dial waits for a name to resolve. It's apart
// from the connect timeout: a bare name that DNS doesn't know falls back to
// LLMNR or mDNS, which can take seconds on Windows, but ssh still waits for it.
const lookupTimeout = 10 * time.Second

// lookupHost resolves a name; tests replace it.
var lookupHost = net.DefaultResolver.LookupHost

// lastAddrs holds each name's last good lookup, used when a lookup fails:
// multicast name lookups don't always answer.
var lastAddrs sync.Map // host → []string

// Dial resolves host, connects to its port and hangs up. The timeout is
// for the connect, not the lookup.
func Dial(ctx context.Context, host string, port int, timeout time.Duration) Reach {
	if port == 0 {
		port = 22
	}
	addrs, stale, err := lookup(ctx, host)
	if err != nil {
		return Reach{Err: ShortNetErr(err), At: time.Now()}
	}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	r := Reach{Stale: stale}
	for _, addr := range addrs {
		r.Addr = addr
		var conn net.Conn
		conn, err = d.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
		if err == nil {
			conn.Close()
			r.Up, r.Latency, r.At = true, time.Since(start), time.Now()
			return r
		}
	}
	r.Err, r.At = ShortNetErr(err), time.Now()
	return r
}

// lookup resolves host, falling back to its last good answer (stale).
func lookup(ctx context.Context, host string) (addrs []string, stale bool, err error) {
	if net.ParseIP(host) != nil {
		return []string{host}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	addrs, err = lookupHost(ctx, host)
	if err == nil && len(addrs) > 0 {
		lastAddrs.Store(host, addrs)
		return addrs, false, nil
	}
	if old, ok := lastAddrs.Load(host); ok {
		return old.([]string), true, nil
	}
	if err == nil {
		err = errors.New("lookup " + host + ": no such host")
	}
	return nil, false, err
}

// ShortNetErr names a connection error in a word or two.
func ShortNetErr(err error) string {
	s := strings.ToLower(err.Error())
	timeout := strings.Contains(s, "timeout") || strings.Contains(s, "timed out") || strings.Contains(s, "deadline exceeded")
	switch {
	case strings.Contains(s, "refused"):
		return "refused"
	case strings.Contains(s, "lookup ") && timeout:
		return "slow dns"
	case timeout:
		return "timeout"
	case strings.Contains(s, "no such host"), strings.Contains(s, "could not resolve"), strings.Contains(s, "name or service not known"):
		return "no dns"
	case strings.Contains(s, "unreachable"):
		return "unreachable"
	}
	return "down"
}

// Outcome is what a login check found.
type Outcome int

const (
	Unknown Outcome = iota
	OK
	NeedsAuth      // key auth didn't work; an interactive ssh may still (password, passphrase)
	HostKeyUnknown // never connected: the host key isn't in known_hosts yet
	HostKeyChanged // the host key doesn't match known_hosts
	Refused
	Timeout
	NoDNS
	Failed
)

func (o Outcome) String() string {
	return [...]string{"not checked", "login works", "needs a password or passphrase", "host key not known yet",
		"host key changed", "connection refused", "timed out", "hostname doesn't resolve", "failed"}[o]
}

// Login is the result of a login check.
type Login struct {
	Outcome Outcome
	Detail  string // ssh's explanation, for failures
	At      time.Time
}

// batch are the options for an ssh that must never wait for a person.
var batch = []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8"}

// CheckLogin connects to alias with key authentication only and runs
// "exit 0", which works in sh, cmd and PowerShell alike.
func CheckLogin(ctx context.Context, r run.Runner, o resolve.Options, alias string) Login {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append(o.ArgsWith(batch, alias), "exit 0")
	out, err := r.Combined(ctx, o.Program(), args...)
	if ctx.Err() != nil {
		return Login{Outcome: Timeout, Detail: "no answer in 20s", At: time.Now()}
	}
	l := Classify(string(out), err)
	l.At = time.Now()
	return l
}

// Classify reads ssh's output and error. ssh exits 255 for its own
// failures; anything else is the remote command's status, so the login
// itself worked.
func Classify(out string, err error) Login {
	if err == nil {
		return Login{Outcome: OK}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() != 255 {
		return Login{Outcome: OK}
	}
	low := strings.ToLower(out)
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(low, x) {
				return true
			}
		}
		return false
	}
	detail := explanation(out)
	switch {
	case has("remote host identification has changed", "has changed and you have requested strict checking"):
		return Login{Outcome: HostKeyChanged, Detail: detail}
	case has("host key is known", "host key verification failed"):
		return Login{Outcome: HostKeyUnknown, Detail: detail}
	case has("permission denied", "too many authentication failures"):
		return Login{Outcome: NeedsAuth, Detail: detail}
	case has("connection refused"):
		return Login{Outcome: Refused, Detail: detail}
	case has("timed out", "timeout"):
		return Login{Outcome: Timeout, Detail: detail}
	case has("could not resolve hostname", "name or service not known", "no such host"):
		return Login{Outcome: NoDNS, Detail: detail}
	}
	if detail == "" {
		detail = err.Error()
	}
	return Login{Outcome: Failed, Detail: detail}
}

// explanation is the line of ssh's output that says what went wrong,
// skipping the warning banner and notices.
func explanation(out string) string {
	var last string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "", strings.HasPrefix(l, "@"), strings.HasPrefix(l, "Warning: Permanently added"),
			strings.HasPrefix(l, "Pseudo-terminal"):
			continue
		}
		last = l
	}
	return last
}

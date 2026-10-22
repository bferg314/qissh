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
	"time"

	"github.com/bferg314/qissh/internal/resolve"
	"github.com/bferg314/qissh/internal/run"
)

// Reach is the result of a TCP connect to a host's ssh port.
type Reach struct {
	Up      bool
	Latency time.Duration
	Err     string // short: "refused", "timeout", "no dns", "unreachable", or ssh's words
	At      time.Time
}

// Dial connects to host:port and hangs up.
func Dial(ctx context.Context, host string, port int, timeout time.Duration) Reach {
	if port == 0 {
		port = 22
	}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	r := Reach{At: time.Now()}
	if err != nil {
		r.Err = ShortNetErr(err)
		return r
	}
	conn.Close()
	r.Up, r.Latency = true, time.Since(start)
	return r
}

// ShortNetErr names a connection error in a word or two.
func ShortNetErr(err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "refused"):
		return "refused"
	case strings.Contains(s, "timeout"), strings.Contains(s, "timed out"):
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

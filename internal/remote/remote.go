// Package remote runs commands on hosts in the background: a command on
// many hosts at once, and the facts (OS, uptime, load, memory, disk,
// pending updates) shown in the detail pane. Like the login check, ssh
// runs with BatchMode and no terminal, so it never stops at a prompt.
package remote

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bferg314/quick-ssh/internal/resolve"
	"github.com/bferg314/quick-ssh/internal/run"
)

var batch = []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8"}

// Result is what a command printed on one host.
type Result struct {
	Alias  string
	Output string // standard output and error together
	// Exit is the command's exit status; -1 when ssh itself failed
	// (couldn't connect or log in) or the command timed out.
	Exit     int
	Err      error // set when Exit is -1
	Duration time.Duration
}

// OK reports whether the command ran and exited 0.
func (r Result) OK() bool { return r.Err == nil && r.Exit == 0 }

// Run runs command on alias and waits at most timeout.
func Run(ctx context.Context, r run.Runner, o resolve.Options, alias, command string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	out, err := r.Combined(ctx, o.Program(), append(o.ArgsWith(batch, alias), command)...)
	res := Result{Alias: alias, Output: string(out), Duration: time.Since(start)}
	var exit interface{ ExitCode() int } // *exec.ExitError, or a fake's
	switch {
	case ctx.Err() != nil:
		res.Exit, res.Err = -1, errors.New("timed out after "+timeout.String())
	case err == nil:
	case errors.As(err, &exit) && exit.ExitCode() != 255:
		res.Exit = exit.ExitCode()
	default:
		// ssh's own failure: its message is the last line it printed.
		res.Exit = -1
		res.Err = err
		if line := lastLine(res.Output); line != "" {
			res.Err = errors.New(line)
		}
	}
	return res
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

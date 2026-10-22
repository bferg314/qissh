// Package run is how qissh calls ssh and its helper programs. All of
// it goes through Runner so tests can swap in a fake.
package run

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Runner runs commands.
type Runner interface {
	// Output runs a command and returns its standard output. A failure
	// carries the first line of standard error.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Combined runs a command and returns standard output and standard
	// error together, for commands whose complaints need reading whole.
	Combined(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Exec runs real programs.
type Exec struct{}

// Output runs name with args. A failure carries the first line of the
// command's standard error, which is where ssh explains itself.
func (Exec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	noTerminal(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if line := FirstLine(stderr.String()); line != "" {
			return out, errors.New(line)
		}
	}
	return out, err
}

// Combined runs name with args and returns everything it printed.
func (Exec) Combined(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	noTerminal(cmd)
	return cmd.CombinedOutput()
}

// FirstLine is the first non-empty line of s, trimmed.
func FirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// NoTerminal makes cmd start without a terminal or console, so ssh can't
// prompt behind the TUI.
func NoTerminal(cmd *exec.Cmd) { noTerminal(cmd) }

// Package run is how quick-ssh calls ssh and its helper programs. All of
// it goes through Runner so tests can swap in a fake.
package run

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Runner runs a command and returns its standard output.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Exec runs real programs.
type Exec struct{}

// Output runs name with args. A failure carries the first line of the
// command's standard error, which is where ssh explains itself.
func (Exec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
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

// FirstLine is the first non-empty line of s, trimmed.
func FirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

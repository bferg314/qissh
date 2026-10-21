//go:build unix

package run

import (
	"os/exec"
	"syscall"
)

// noTerminal starts cmd in a new session, without a controlling terminal,
// so ssh can't open /dev/tty to ask for a password behind the TUI.
func noTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

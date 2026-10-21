package run

import (
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008

// noTerminal starts cmd without a console, so ssh can't read a password
// from one behind the TUI.
func noTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
}

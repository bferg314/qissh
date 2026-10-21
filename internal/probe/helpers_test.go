package probe

import (
	"os/exec"
	"strconv"
)

func itoa(n int) string { return strconv.Itoa(n) }

// Exec runs a command and returns its output and error.
func Exec(cmd ...string) ([]byte, error) { return exec.Command(cmd[0], cmd[1:]...).CombinedOutput() }

//go:build unix

package runner

import (
	"os/exec"
	"syscall"
	"time"
)

// killGroup makes cancelling a command kill everything it started, not only
// the shell.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}

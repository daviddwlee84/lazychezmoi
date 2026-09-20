//go:build !windows

package chezmoi

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Only background/read processes get their own process group. Interactive
// handoffs must remain in the terminal's foreground process group.
func configureReadProcess(cmd *exec.Cmd) {
	if cmd.Stdin != nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

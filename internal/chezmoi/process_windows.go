//go:build windows

package chezmoi

import (
	"os"
	"os/exec"
	"strconv"
)

func configureReadProcess(cmd *exec.Cmd) {
	if cmd.Stdin != nil {
		return
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.Env = ChildEnv()
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

//go:build !windows

package project

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureGitProcess 让 git 及其子进程（如外部 diff/textconv 助手）随取消一起终止，
// 并限制已脱离的子进程通过继承管道拖住 Wait。
func configureGitProcess(cmd *exec.Cmd) {
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
	cmd.WaitDelay = 2 * time.Second
}

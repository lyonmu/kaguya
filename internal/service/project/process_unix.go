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

// openReadFile 以非阻塞方式打开待检查的路径：FIFO 无写端时立即返回错误，
// 而不是让预览请求永久阻塞在 open 上。
func openReadFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// clearNonblock 打开成功后恢复阻塞模式，普通文件的后续读取不受影响。
func clearNonblock(file *os.File) {
	_ = syscall.SetNonblock(int(file.Fd()), false)
}

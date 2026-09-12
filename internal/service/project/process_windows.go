//go:build windows

package project

import (
	"os"
	"os/exec"
)

// configureGitProcess 在 Windows 上没有进程组语义，超时由 CommandContext 处理。
func configureGitProcess(cmd *exec.Cmd) {}

func openReadFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}

func clearNonblock(*os.File) {}

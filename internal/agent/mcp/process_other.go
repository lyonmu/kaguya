//go:build !unix

package mcp

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}

//go:build unix

package project

import (
	"os"
	"syscall"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
)

func fileSearchIdentity(_ *os.File, info os.FileInfo) (fileSearchRootID, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileSearchRootID{}, workspace.ErrReferenceUnavailable
	}
	return fileSearchRootID{Volume: uint64(stat.Dev), Object: uint64(stat.Ino)}, nil
}

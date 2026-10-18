//go:build windows

package project

import (
	"os"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
	"golang.org/x/sys/windows"
)

func fileSearchIdentity(file *os.File, _ os.FileInfo) (fileSearchRootID, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return fileSearchRootID{}, workspace.ErrReferenceUnavailable
	}
	return fileSearchRootID{Volume: uint64(info.VolumeSerialNumber), Object: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}, nil
}

//go:build !unix && !windows

package project

import (
	"os"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
)

func fileSearchIdentity(*os.File, os.FileInfo) (fileSearchRootID, error) {
	return fileSearchRootID{}, workspace.ErrReferenceUnavailable
}

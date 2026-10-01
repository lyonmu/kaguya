//go:build windows

package instructions

import "os"

func openNonblock(path string) (*os.File, error) { return os.Open(path) }

func clearNonblock(*os.File) {}

//go:build !darwin

package cmd

import "fmt"

// runDesktop 在非 macOS 平台上不构建原生窗口；显式 --web 仍可运行 Web 服务。
func runDesktop() error {
	return fmt.Errorf("kaguya desktop is only available on macOS; start the HTTP service with --web")
}

//go:build darwin

package desktop

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// loginShellTimeout 限制读取登录 shell 环境的耗时；超时或失败时保留现有 PATH。
const loginShellTimeout = 5 * time.Second

// AdoptLoginShellPath 用登录 shell 的 PATH 补充 Desktop 进程环境。
//
// Finder／LaunchServices 启动的应用由 launchd 直接派生（父进程为 1），默认 PATH
// 只有系统目录，nvm、Homebrew、bun、uvx 等用户工具不可见，Bash 工具与 stdio MCP
// 都会遇到“找不到可执行文件”。这里只在 GUI 启动时执行一次登录 shell 并读取 PATH
// 合并到进程环境；从终端启动时已经继承用户环境，不做处理。
func AdoptLoginShellPath() {
	if os.Getppid() != 1 {
		return
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginShellTimeout)
	defer cancel()
	loginPath, err := readLoginShellPath(ctx, shell)
	if err != nil {
		return
	}
	current := os.Getenv("PATH")
	if merged := mergePath(loginPath, current); merged != current {
		_ = os.Setenv("PATH", merged)
	}
}

// readLoginShellPath 运行一次交互式登录 shell 并解析其中的 PATH。
// 使用 env 而不是 echo "$PATH"，兼容 zsh、bash 与 fish 等不同 shell。
func readLoginShellPath(ctx context.Context, shell string) (string, error) {
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", "command env")
	// 登录脚本可能派生长驻子进程并占用管道；超时后不再等待它们。
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return parseLoginShellPath(output), nil
}

// parseLoginShellPath 从 shell 输出中取出 PATH 行；登录脚本的其他输出不参与解析。
func parseLoginShellPath(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		if value, ok := strings.CutPrefix(line, "PATH="); ok {
			return strings.TrimSuffix(value, "\r")
		}
	}
	return ""
}

// mergePath 把登录 shell 的 PATH 放在前面，并追加当前 PATH 中尚未出现的目录。
func mergePath(loginPath, current string) string {
	seen := map[string]bool{}
	entries := make([]string, 0, 16)
	for _, group := range []string{loginPath, current} {
		for _, entry := range strings.Split(group, ":") {
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	return strings.Join(entries, ":")
}

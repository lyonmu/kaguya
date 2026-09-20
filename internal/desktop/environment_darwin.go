//go:build darwin

package desktop

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// loginShellTimeout 限制读取登录 shell 环境的耗时；超时或失败时保留现有环境。
const loginShellTimeout = 5 * time.Second

// AdoptLoginShellEnvironment 用登录 shell 环境补充 Desktop 进程环境。
//
// Finder／LaunchServices 启动的应用由 launchd 直接派生（父进程为 1），不会读取
// ~/.zshrc。这里在 GUI 启动时显式加载 ~/.zshrc，并把其中导出的完整环境导入应用，
// 供 Bash 工具与 stdio MCP 的子进程继承；从终端启动时已经继承用户环境，不做处理。
func AdoptLoginShellEnvironment() {
	if os.Getppid() != 1 {
		return
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginShellTimeout)
	defer cancel()
	loginEnv, err := readLoginShellEnvironment(ctx, shell)
	if err != nil {
		return
	}
	adoptLoginShellEnvironment(loginEnv)
}

// readLoginShellEnvironment 显式加载 ~/.zshrc，再读取它导出的完整环境。
// NUL 分隔允许环境变量值包含换行符，加载脚本的普通输出不会混入解析结果。
func readLoginShellEnvironment(ctx context.Context, shell string) (map[string]string, error) {
	const command = `source "$HOME/.zshrc" >/dev/null 2>&1; /usr/bin/env -0`
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", command)
	// 登录脚本可能派生长驻子进程并占用管道；超时后不再等待它们。
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseLoginShellEnvironment(output), nil
}

func parseLoginShellEnvironment(output []byte) map[string]string {
	environment := make(map[string]string)
	for _, entry := range bytes.Split(output, []byte{0}) {
		name, value, ok := bytes.Cut(entry, []byte{'='})
		if ok && len(name) > 0 {
			environment[string(name)] = string(value)
		}
	}
	return environment
}

func adoptLoginShellEnvironment(loginEnv map[string]string) {
	for name, value := range loginEnv {
		_ = os.Setenv(name, value)
	}
}

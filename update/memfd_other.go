//go:build !linux

package update

import "fmt"

// applyViaRelay 非 Linux 没有 memfd 也没有 /proc/self/fd 那套，直接走旁路方案
func applyViaRelay(t target, p *Plan) error {
	return fmt.Errorf("%w：非 Linux 系统", errNoRelay)
}

// HandleInstall 安装模式只有 Linux 的接力更新会用到
func HandleInstall(args []string) bool { return false }

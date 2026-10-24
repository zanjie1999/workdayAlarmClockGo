//go:build !windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Restart 用新文件替换掉自己并重新执行，不会返回
func Restart() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if err := os.Chmod(self, 0755); err != nil {
		return fmt.Errorf("设置执行权限失败：%w", err)
	}
	return syscall.Exec(self, os.Args, os.Environ())
}

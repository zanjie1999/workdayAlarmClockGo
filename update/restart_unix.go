//go:build !windows

package update

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// Restart 用新文件替换掉自己并重新执行，不会返回
func Restart() error {
	// 必须用启动时缓存下来的路径：更新时原文件已经被改名成 xxx.old，
	self, err := SelfExe()
	if err != nil {
		return err
	}
	fi, err := os.Stat(self)
	if err != nil {
		return fmt.Errorf("找不到要执行的程序：%w", err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%s 是目录，无法执行", self)
	}
	if err := os.Chmod(self, 0755); err != nil {
		return fmt.Errorf("设置执行权限失败：%w", err)
	}
	log.Println("即将重启，可执行文件：", self)
	return syscall.Exec(self, os.Args, os.Environ())
}

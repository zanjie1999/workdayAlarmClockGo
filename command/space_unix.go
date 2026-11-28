//go:build !windows

package command

import (
	"syscall"
)

// freeSpaceMB 取目录所在分区的剩余空间（MB），失败返回 -1。
func freeSpaceMB(dir string) int {
	if dir == "" {
		dir = "."
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int(uint64(st.Bavail) * uint64(st.Bsize) / (1024 * 1024))
}

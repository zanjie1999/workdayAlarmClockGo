//go:build !windows

package update

import (
	"os"
	"syscall"
)

// freeSpace 返回 dir 所在文件系统当前可用的字节数。
// 以 root 跑的时候把保留块也算上，否则空间吃紧的设备会白白多留一截不让用。
func freeSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	bsize := uint64(st.Bsize)
	if os.Geteuid() == 0 {
		return uint64(st.Bfree) * bsize, nil
	}
	return uint64(st.Bavail) * bsize, nil
}

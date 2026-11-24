//go:build windows

package update

import "errors"

// freeSpace Windows 下用不上（更新只能手动替换），这里只为让包能编译
func freeSpace(dir string) (uint64, error) { return 0, errors.New("Windows 下不支持") }

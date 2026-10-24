//go:build windows

package update

import "errors"

// Restart Windows 下无法重新 exec 自己，只能让用户手动重启
func Restart() error {
	return errors.New("Windows 下请手动关闭并重新运行程序")
}

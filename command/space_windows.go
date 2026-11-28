//go:build windows

package command

// freeSpaceMB Windows 下没有对应的 syscall 写法，直接报读不到。
func freeSpaceMB(string) int { return -1 }

//go:build linux

package player

import (
	"net"
	"os"
	"syscall"
	"unsafe"
)

// FIONREAD/TIOCINQ，Linux 各架构都是这个值。
const ioctlFionRead = 0x541B

// fcntl 的管道容量命令。
const (
	pipeGetSizeCmd = 1032 // F_GETPIPE_SZ
	pipeSetSizeCmd = 1031 // F_SETPIPE_SZ
)

func init() {
	socketUnread = func(conn net.Conn) int {
		tcp, ok := conn.(*net.TCPConn)
		if !ok {
			return -1
		}
		count := int32(0)
		if !rawFd(tcp.SyscallConn, func(fd uintptr) {
			if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlFionRead,
				uintptr(unsafe.Pointer(&count))); errno != 0 {
				count = -1
			}
		}) {
			return -1
		}
		return int(count)
	}
}

// pipeBacklogBytes 返回管道里还没被 aplay 读走的字节数，失败返回 -1。
// FIONREAD 在管道写端一样能查到整条管道的积压。
func pipeBacklogBytes(pipe *os.File) int {
	if pipe == nil {
		return -1
	}
	count := int32(0)
	if !rawFd(pipe.SyscallConn, func(fd uintptr) {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlFionRead,
			uintptr(unsafe.Pointer(&count))); errno != 0 {
			count = -1
		}
	}) {
		return -1
	}
	return int(count)
}

// pipeCapacityBytes 返回管道容量，失败返回 -1。
func pipeCapacityBytes(pipe *os.File) int {
	return pipeFcntl(pipe, pipeGetSizeCmd, 0)
}

// shrinkPipe 把管道改小，返回改后的容量，失败返回 -1。
// 播放器子进程的 stdin 管道默认有 64KB，等于平白多出几百毫秒延迟。
func shrinkPipe(pipe *os.File, size int) int {
	if size <= 0 {
		return -1
	}
	return pipeFcntl(pipe, pipeSetSizeCmd, size)
}

func pipeFcntl(pipe *os.File, cmd int, arg int) int {
	if pipe == nil {
		return -1
	}
	result := -1
	if !rawFd(pipe.SyscallConn, func(fd uintptr) {
		value, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, uintptr(cmd), uintptr(arg))
		if errno == 0 {
			result = int(value)
		}
	}) {
		return -1
	}
	return result
}

// rawFd 通过 SyscallConn 拿文件描述符，避免 Fd() 把文件切成阻塞模式。
func rawFd(syscallConn func() (syscall.RawConn, error), fn func(fd uintptr)) bool {
	raw, err := syscallConn()
	if err != nil {
		return false
	}
	return raw.Control(fn) == nil
}

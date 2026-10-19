//go:build linux

package player

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// alsaBufferFrames 从 /proc/asound 读正在播放的 PCM 的 buffer_size。
// aplay 是子进程，父进程看不到它的 ALSA 缓冲，但内核把每个 pcm 的 hw_params
// 都暴露在 /proc 里，读一次就能拿到真实值(请求 40ms 常被驱动取整到 80ms+)。
// 按 rate/channels 过滤，避免拿到别的进程正在用的那个 pcm。
// 找不到返回 0，调用方退回保守估算。
func alsaBufferFrames(rate, channels int) int {
	subdirs, err := filepath.Glob("/proc/asound/card*/pcm*p/sub*/hw_params")
	if err != nil {
		return 0
	}
	rateKey := "rate: " + strconv.Itoa(rate)
	channelKey := "channels: " + strconv.Itoa(channels)
	for _, path := range subdirs {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		if !strings.Contains(text, rateKey) || !strings.Contains(text, channelKey) {
			continue
		}
		if frames := parseHwParamsInt(text, "buffer_size:"); frames > 0 {
			return frames
		}
	}
	return 0
}

// parseAplayBufferFrames 从 aplay -v 的 stderr 里取 buffer_size。
// /proc/asound 读不到时(容器、精简系统)这是唯一的来源：
// aplay 会打印 "buffer_size   : 4096"。
func parseAplayBufferFrames(text string) int {
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "buffer_size") {
			continue
		}
		if _, value, ok := strings.Cut(line, ":"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// parseHwParamsInt 从 hw_params 文本里取出某个字段的整数值。
func parseHwParamsInt(text, key string) int {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return 0
}

//go:build linux

package player

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// FIONREAD/TIOCINQ 的 ioctl 号在各架构上并不相同：
// x86/arm/arm64/riscv 是 0x541B，mips 是 0x467F，ppc/sparc 是 0x4004667F。
// 写死成 0x541B 在 mips 上会 ioctl 失败，FIONREAD 永远拿不到，
// 稳态的排队结算就彻底失效——所以直接用标准库按架构准备好的常量。
const ioctlFionRead = syscall.TIOCINQ

// /proc/asound 的 status 最多这么久刷新一次，避免小机器上每 15ms 开一次文件。
const pcmSinkProbeGap = 50 * time.Millisecond

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
		if frames := parseStatusInt(text, "buffer_size"); frames > 0 {
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

// parseStatusInt 从 /proc/asound 的 status / hw_params 文本里取出某个字段的整数值。
// 字段行形如 "delay: 1234"、"owner_pid   : 5678"、"rate: 48000 (48000/1)"，
// 冒号周围空格数不固定，所以统一按第一个冒号切开再取第一个词。取不到返回 -1。
func parseStatusInt(text, key string) int {
	value, ok := parseStatusValue(text, key)
	if !ok {
		return -1
	}
	if space := strings.IndexByte(value, ' '); space >= 0 {
		value = value[:space]
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return n
}

// parseStatusValue 取出某个字段冒号后面的原文(已去掉首尾空白)。
func parseStatusValue(text, key string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return strings.TrimSpace(value), true
	}
	return "", false
}

// pcmSinkProbe 通过 /proc/asound 读 aplay 子进程那个 pcm 的实时排队。
// status 文件里的 delay 就是"已经写进驱动、还没播出来"的帧数，比按管道占用
// 比例折算声卡缓冲准得多(实测误差能到上百毫秒)。/proc 读不到(容器、精简
// rootfs)时 valid=false，调用方退回原来的估算方式，播放行为不变。
type pcmSinkProbe struct {
	path       string
	frameBytes int
	lastRead   time.Time
	delayBytes int64
	valid      bool
	state      string
	xruns      int64
}

// newPCMStatusProbe 用 aplay 子进程的 pid 定位它正在用的那个 pcm 的 status 文件。
// 定位不到、或平台没有 /proc/asound 时返回 nil，调用方全程走旧估算。
func newPCMStatusProbe(pid, rate, channels, frameBytes int) *pcmSinkProbe {
	path := findPCMStatusFile(pid, rate, channels)
	if path == "" {
		return nil
	}
	probe := &pcmSinkProbe{path: path, frameBytes: frameBytes}
	if probe.refresh(time.Now()) < 0 {
		// 文件在但读不出 delay(设备刚被别的进程抢走之类)，不如不猜
		return nil
	}
	log.Printf("aplay: 声卡排队改用 %s 实测", path)
	return probe
}

// findPCMStatusFile 优先按 owner_pid 精确定位，退回用 hw_params 的 rate/channels 匹配。
func findPCMStatusFile(pid, rate, channels int) string {
	statuses, err := filepath.Glob("/proc/asound/card*/pcm*p/sub*/status")
	if err != nil {
		return ""
	}
	rateKey := "rate: " + strconv.Itoa(rate)
	channelKey := "channels: " + strconv.Itoa(channels)
	fallback := ""
	for _, path := range statuses {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(data)
		if parseStatusInt(text, "owner_pid") == pid {
			return path
		}
		if fallback != "" {
			continue
		}
		hw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "hw_params"))
		if err != nil {
			continue
		}
		hwText := string(hw)
		if strings.Contains(hwText, rateKey) && strings.Contains(hwText, channelKey) {
			fallback = path
		}
	}
	return fallback
}

// refresh 最多每 pcmSinkProbeGap 读一次 status，返回声卡侧排队字节数，
// 未知返回 -1。顺带累计 XRUN，用来解释"卡顿期间时间轴已经被吃掉"。
func (p *pcmSinkProbe) refresh(now time.Time) int64 {
	if p == nil || p.path == "" {
		return -1
	}
	if !p.lastRead.IsZero() && now.Sub(p.lastRead) < pcmSinkProbeGap {
		if !p.valid {
			return -1
		}
		return p.delayBytes
	}
	p.lastRead = now
	data, err := os.ReadFile(p.path)
	if err != nil {
		// pcm 已经关了，别每轮重试刷屏
		p.path = ""
		p.valid = false
		return -1
	}
	text := string(data)
	if state, ok := parseStatusValue(text, "state"); ok {
		if state == "XRUN" && p.state != "XRUN" {
			p.xruns++
			log.Printf("aplay: 声卡欠载(XRUN)，卡顿期间的时间轴已经被吃掉，不再按卡顿时长补丢")
		}
		p.state = state
	}
	frames := parseStatusInt(text, "delay")
	if frames < 0 {
		p.valid = false
		return -1
	}
	p.delayBytes = int64(frames) * int64(p.frameBytes)
	p.valid = true
	return p.delayBytes
}

// drainXRuns 取走并清零本轮的欠载计数。
func (p *pcmSinkProbe) drainXRuns() int64 {
	if p == nil {
		return 0
	}
	count := p.xruns
	p.xruns = 0
	return count
}

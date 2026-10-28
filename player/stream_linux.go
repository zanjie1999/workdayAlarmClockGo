//go:build linux && !app

package player

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// 播放器子进程 stdin 管道的容量。默认 64KB ≈ 370ms，等于凭空多出一大截延迟，
// 实时流只需要放下一块音频(一页 4KB ≈ 21ms @48kHz 立体声)。
const pcmPipeBytes = 2 << 10

// 各 rate/声道组合的声卡缓冲字节数，启动时探测一次就记住。
// aplay -B 40000 常被驱动向上取整(实测 40ms 请求给回 85ms)，
// 按请求值算会让 read() 多丢掉几十毫秒好音频。
var aplayCardBytes sync.Map

// probeAplayCardBytes 拿 aplay 侧声卡缓冲的真实大小。
// 先读 /proc/asound(不用起进程)，读不到再起一个 aplay 用 -v 打出来，都不行就用保守值。
func probeAplayCardBytes(rate, channels int) int64 {
	frameBytes := channels * 2
	key := strconv.Itoa(rate) + "/" + strconv.Itoa(channels)
	if cached, ok := aplayCardBytes.Load(key); ok {
		return cached.(int64)
	}
	var cardBytes int64
	if frames := alsaBufferFrames(rate, channels); frames > 0 {
		cardBytes = int64(frames) * int64(frameBytes)
	} else if frames := runAplayProbe(rate, channels); frames > 0 {
		cardBytes = int64(frames) * int64(frameBytes)
	}
	if cardBytes <= 0 {
		cardBytes = msBytes(rate, frameBytes, pcmSinkBufferMs)
	}
	aplayCardBytes.Store(key, cardBytes)
	log.Printf("aplay: 声卡缓冲 %dms(%dHz %d声道)", cardBytes*1000/int64(frameBytes)/int64(rate), rate, channels)
	return cardBytes
}

// runAplayProbe 喂一小段静音给 aplay，让它把实际生效的 hw_params(含 buffer_size)打出来。
// 只跑一次，结果会被 probeAplayCardBytes 缓存。
func runAplayProbe(rate, channels int) int {
	frameBytes := channels * 2
	cmd := exec.Command(ShellPlayer, "-v", "-D", alsaDevice(), "-t", "raw", "-f", "S16_LE",
		"-c", strconv.Itoa(channels), "-r", strconv.Itoa(rate),
		"-B", "40000", "-F", "10000")
	cmd.Stdin = bytes.NewReader(make([]byte, msBytes(rate, frameBytes, 60)))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return 0
	}
	return parseAplayBufferFrames(out.String())
}

func playPCMStream(r io.Reader, rate, channels int, remoteAddr string) error {
	switch filepath.Base(ShellPlayer) {
	case "alsa":
		// 直接 ALSA 后端(cgo 构建)：没有子进程管道，声卡排队由 ALSA 自己报
		return alsaPlayPCMStream(r, rate, channels, remoteAddr)
	case "aplay":
	default:
		return ErrPCMStreamUnsupported
	}
	frameBytes := channels * 2
	cmd := exec.Command(ShellPlayer, "-q", "-D", alsaDevice(), "-t", "raw", "-f", "S16_LE",
		"-c", strconv.Itoa(channels), "-r", strconv.Itoa(rate),
		"-B", "40000", "-F", "10000")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	pipe, _ := stdin.(*os.File)
	pipeSize := shrinkPipe(pipe, pcmPipeBytes)
	if pipeSize < 0 {
		pipeSize = pipeCapacityBytes(pipe)
	}
	// 管道 + aplay 自己的声卡缓冲，是这条链路不可能再低下去的排队
	sinkBytes := probeAplayCardBytes(rate, channels)
	if pipeSize > 0 {
		sinkBytes += int64(pipeSize)
	}
	queue := newPCMQueue(rate, frameBytes, sinkBytes)
	queue.backend = "aplay"
	log.Printf("aplay: 实时流 %dHz %d声道，管道 %d 字节，目标排队 %dms",
		rate, channels, pipeSize, queue.ms(queue.target))
	defer queue.clearPCMStats()

	// 量不到内核接收缓冲时，稳态 fix() 会完全失效，只剩"按卡顿时长追平"这一条路。
	// 这里把到底卡在哪一步打出来，省得以后靠猜。
	if conn, ok := streamConn(remoteAddr); !ok {
		log.Printf("aplay: 没登记到连接 %s，读不到内核接收缓冲，只按卡顿时长追平", remoteAddr)
	} else if unreadBytes(remoteAddr) < 0 {
		log.Printf("aplay: 连接类型 %T 取不到 FIONREAD，只按卡顿时长追平", conn)
	}

	// EQ 按当前流的采样率重算系数，并清掉上一条流残留的滤波状态。
	// 实例每条流只取一次(GetEQ 走 sync.Once)，Process 仍然每块调用：
	// 里面只是一次原子读加一个分支，比省掉的那次整块拷贝便宜得多，
	// 而且这样播放中途打开均衡器能立刻生效。
	eq := GetEQ()
	eq.SetSampleRate(rate)
	ResetEQState()

	// 子进程一定要回收，否则每次断流都会留下一个僵尸 aplay。
	// 原来只在 copyErr != nil 时 Kill，正常路径走 stdin.Close() 再 Wait()，
	// 而 Close() 在 aplay 不再读管道时可能永久阻塞，Wait() 就永远到不了，
	// 于是进程变僵尸。这里用 defer 兜底，任何返回路径都保证回收。
	reaped := false
	defer func() {
		if reaped {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	copyErr := copyPCMStream(r, stdin, queue, remoteAddr, pipe, pipeSize, cmd.Process.Pid, eq)

	// 连接一断就立刻杀掉 aplay，不等它把管道/声卡缓冲里剩下的音频播完。
	// 实时流场景下，"播完残留"没有任何意义：上游已经断了，残留放出来的
	// 只会是过期音频，还会让这台设备比别的设备多拖一截。
	// 顺序也很重要：先 Kill 再 Close，避免 Close 阻塞(见上面的 defer 注释)。
	_ = cmd.Process.Kill()
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	reaped = true

	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}

func copyPCMStream(r io.Reader, stdin io.Writer, queue *pcmQueue, remoteAddr string, pipe *os.File, pipeSize int, pid int, eq *EQ) error {
	buf := make([]byte, msBytes(queue.rate, queue.frameBytes, pcmReadChunkMs))
	// pending 只是跨块残帧的暂存区，正常情况长度一直是 0
	pending := make([]byte, 0, len(buf)+queue.frameBytes)
	emptyReads := 0
	// aplay 启动瞬间可能还没 open 设备，那时 /proc/asound 里找不到它的 pcm。
	// 所以第一次读到数据之后再定位一次，之后每轮只读这一个 status 文件。
	var probe *pcmSinkProbe
	probeTried := false
	src := pcmSource{
		socket: func() int64 { return unreadBytes(remoteAddr) },
		sink:   func() int64 { return pipeQueuedBytes(pipe, pipeSize, queue, probe) },
	}
	// flush 把一块按帧对齐的数据过 EQ 后写进管道并记账
	flush := func(data []byte) error {
		eq.Process(data)
		if _, err := stdin.Write(data); err != nil {
			return err
		}
		queue.writtenBytes += int64(len(data))
		return nil
	}
	for {
		n, skip, readErr := queue.step(r, buf, src)
		if !probeTried && n > 0 {
			probeTried = true
			probe = newPCMStatusProbe(pid, queue.rate, queue.frameBytes/2, queue.frameBytes)
		}
		if count := probe.drainXRuns(); count > 0 {
			queue.xruns += count
		}
		if n > 0 {
			emptyReads = 0
		}
		if skip {
			// 追赶丢的也是流里的字节，必须和稳态一样按帧拼接：Read 返回多少字节
			// 是随机的，n 不是 frameBytes 的整数倍时，扔掉的余数会让之后所有数据的
			// 帧边界整体错位——左右声道交错，听起来就是白噪音。
			if len(pending) == 0 {
				aligned := n / queue.frameBytes * queue.frameBytes
				pending = append(pending[:0], buf[aligned:n]...)
			} else {
				pending = append(pending, buf[:n]...)
				aligned := len(pending) / queue.frameBytes * queue.frameBytes
				pending = append(pending[:0], pending[aligned:]...)
			}
		} else if n > 0 {
			// 稳态下每块本身就是整帧，直接原地处理原地写，不再整块拷一遍。
			// 只有上游把一帧劈开时，才把不足一帧的残渣(最多 frameBytes-1 字节)
			// 攒到下一块一起送——拷贝量从"整块"降到"几个字节"。
			if len(pending) == 0 {
				aligned := n / queue.frameBytes * queue.frameBytes
				if aligned > 0 {
					if err := flush(buf[:aligned]); err != nil {
						return err
					}
				}
				pending = append(pending[:0], buf[aligned:n]...)
			} else {
				pending = append(pending, buf[:n]...)
				aligned := len(pending) / queue.frameBytes * queue.frameBytes
				if aligned > 0 {
					if err := flush(pending[:aligned]); err != nil {
						return err
					}
					pending = append(pending[:0], pending[aligned:]...)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if len(pending) != 0 {
					return fmt.Errorf("incomplete PCM frame: received %d trailing bytes, expected a multiple of %d",
						len(pending), queue.frameBytes)
				}
				return nil
			}
			return fmt.Errorf("read PCM stream: %w", readErr)
		}
		if n == 0 {
			emptyReads++
			if emptyReads >= 100 {
				return io.ErrNoProgress
			}
		}
	}
}

// pipeQueuedBytes 返回声卡侧当前排队：管道积压 + aplay 写进驱动的未播数据。
// 管道积压是实测的(FIONREAD)；声卡排队优先读 /proc/asound 的 delay，
// 读不到时退回原来的估算。
//
// 原来的估算推断是"管道快满 ⟹ aplay 正堵在写声卡 ⟹ 声卡缓冲也满"，但管道很小时
// 这个等价关系不成立：管道只有 43ms 时，3/4 阈值才 32ms，正常 10ms 一批的节奏
// 稍有抖动就跨过去，于是把声卡缓冲(实测 341ms)全额算进排队，sink 长期顶到
// 384ms 的天花板。而 fix() 里 target = sink + 余量，sink 在两边抵消，
// 结果就是"永远刚好、永远不丢"，多设备一旦错位就再也追不回来。
//
// 现在改成按管道占用比例折算声卡积压：管道越满越接近满缓冲，但不再一步跳满。
// 这样既保留了"管道满时声卡多半也满"的方向性，又不会因为几毫秒抖动就虚高几百毫秒。
func pipeQueuedBytes(pipe *os.File, pipeSize int, queue *pcmQueue, probe *pcmSinkProbe) int64 {
	backlog := pipeBacklogBytes(pipe)
	if backlog < 0 {
		// 管道量不到，只能按缓冲全满估，宁可高估也不低估
		return queue.sinkLimit
	}
	queued := int64(backlog)
	if probe != nil {
		if card := probe.refresh(time.Now()); card >= 0 {
			return queued + card
		}
	}
	cardBytes := queue.sinkLimit - int64(pipeSize)
	if cardBytes <= 0 || pipeSize <= 0 {
		return queued
	}
	// 管道占用比例，夹在 0..1。空管道时声卡积压按 0 算，满管道时才按全额算。
	fill := float64(backlog) / float64(pipeSize)
	if fill > 1 {
		fill = 1
	}
	queued += int64(float64(cardBytes) * fill)
	return queued
}

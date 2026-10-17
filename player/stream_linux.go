//go:build linux && !app

package player

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// 播放器子进程 stdin 管道的容量。默认 64KB ≈ 370ms，等于凭空多出一大截延迟，
// 实时流只需要放下一两块音频(4KB ≈ 23ms @44.1kHz 立体声)。
const pcmPipeBytes = 4 << 10

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
		"-B", "60000", "-F", "10000")
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
	sinkBytes := msBytes(rate, frameBytes, pcmSinkBufferMs)
	if pipeSize > 0 {
		sinkBytes += int64(pipeSize)
	}
	queue := newPCMQueue(rate, frameBytes, sinkBytes)
	log.Printf("aplay: 实时流 %dHz %d声道，管道 %d 字节，目标排队 %dms",
		rate, channels, pipeSize, queue.ms(queue.target))

	copyErr := copyPCMStream(r, stdin, queue, remoteAddr, pipe, pipeSize)
	closeErr := stdin.Close()
	if copyErr != nil {
		// 请求坏了就立刻停，别再把管道里排队的音频放完
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}

func copyPCMStream(r io.Reader, stdin io.Writer, queue *pcmQueue, remoteAddr string, pipe *os.File, pipeSize int) error {
	buf := make([]byte, msBytes(queue.rate, queue.frameBytes, pcmReadChunkMs))
	pending := make([]byte, 0, len(buf)+queue.frameBytes)
	emptyReads := 0
	for {
		// 先量后排、再读：只有还没进管道/声卡的旧音频才丢得掉
		socketBytes := unreadBytes(remoteAddr)
		if err := queue.fix(r, socketBytes, pipeQueuedBytes(pipe, pipeSize, queue)); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return fmt.Errorf("skip PCM stream: %w", err)
		}

		n, readErr := r.Read(buf)
		if n > 0 {
			emptyReads = 0
			queue.readBytes += int64(n)
			pending = append(pending, buf[:n]...)
			aligned := len(pending) / queue.frameBytes * queue.frameBytes
			if aligned > 0 {
				if _, err := stdin.Write(pending[:aligned]); err != nil {
					return err
				}
				queue.writtenBytes += int64(aligned)
				pending = append(pending[:0], pending[aligned:]...)
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

// pipeQueuedBytes 估算声卡侧当前排队：管道积压 + aplay 自己的声卡缓冲。
// 管道满 ⟺ aplay 正堵在写声卡 ⟺ 它缓冲也是满的，所以这时候才算那 60ms；
// 欠载时管道是空的，不把它算进去才不会虚高。
func pipeQueuedBytes(pipe *os.File, pipeSize int, queue *pcmQueue) int64 {
	alsaBytes := msBytes(queue.rate, queue.frameBytes, pcmSinkBufferMs)
	backlog := pipeBacklogBytes(pipe)
	if backlog < 0 {
		return alsaBytes
	}
	queued := int64(backlog)
	if pipeSize <= 0 || int64(backlog) >= int64(pipeSize)*3/4 {
		queued += alsaBytes
	}
	return queued
}

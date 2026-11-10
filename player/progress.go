/*
 * progress.go -- 播放进度 / 音量上报（给 LVGL 外壳用）
 *
 * 进度来源按可信度排序：
 *  1. 已写入声卡的 PCM 字节数。aplay/tinyplay 的管道写、直写 ALSA 的
 *     alsaWritePCM 都收口到 AddPlayedBytes，暂停时写入会停下来，
 *     所以"暂停不涨、切歌从 0 开始"是天然成立的。
 *  2. 兜底：本曲开始时间 - 累计暂停时长。只用于外部播放器路径
 *     （meMp3Player 之类，子进程内部状态看不到）。
 *
 * 总时长由 mp3dur.go 从文件头算；算不出来就是 0，外壳走不确定态。
 */
package player

import (
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
)

var (
	// NowVol 当前音量 0..100。Go 以前只下发不记录，暂停恢复、外壳回显都要它。
	NowVol = 50

	durationMs  int64 // 本曲总时长，0 = 未知
	playedBytes int64 // 已写进声卡的 PCM 字节数
	pcmRate     int32 // 解码后的采样率，0 = 未知

	songStartUnix int64 // 本曲开始时间（StartUnix 是整个会话的，切歌不重置）
	pausedTotalMs int64 // 本曲累计暂停时长
	pausedAtUnix  int64 // 本次暂停开始时间，0 = 没在暂停
	progMu        sync.Mutex
)

// ResetProgress 每首新歌开始时调用
func ResetProgress() {
	atomic.StoreInt64(&playedBytes, 0)
	atomic.StoreInt64(&durationMs, 0)
	atomic.StoreInt32(&pcmRate, 0)

	progMu.Lock()
	songStartUnix = time.Now().Unix()
	pausedTotalMs = 0
	pausedAtUnix = 0
	progMu.Unlock()
}

func SetPCMRate(rate int) {
	if rate > 0 {
		atomic.StoreInt32(&pcmRate, int32(rate))
	}
}

// SetDurationMs 0 表示未知（木有帧头 / 拿不到文件大小）
func SetDurationMs(ms int64) {
	if ms < 0 {
		ms = 0
	}
	atomic.StoreInt64(&durationMs, ms)
}

func DurationMs() int64 { return atomic.LoadInt64(&durationMs) }

// AddPlayedBytes 由各个写入点调用；16bit 双声道 = 4 字节一帧
func AddPlayedBytes(n int64) {
	if n > 0 {
		atomic.AddInt64(&playedBytes, n)
	}
}

func notePause() {
	progMu.Lock()
	if pausedAtUnix == 0 {
		pausedAtUnix = time.Now().Unix()
	}
	progMu.Unlock()
}

func noteResume() {
	progMu.Lock()
	if pausedAtUnix != 0 {
		pausedTotalMs += (time.Now().Unix() - pausedAtUnix) * 1000
		pausedAtUnix = 0
	}
	progMu.Unlock()
}

// PlayedMs 当前播放位置
func PlayedMs() int64 {
	if rate := atomic.LoadInt32(&pcmRate); rate > 0 {
		bytes := atomic.LoadInt64(&playedBytes)
		ms := bytes * 1000 / (4 * int64(rate))
		if d := DurationMs(); d > 0 && ms > d {
			ms = d
		}
		return ms
	}

	progMu.Lock()
	defer progMu.Unlock()
	if songStartUnix == 0 {
		return 0
	}
	ms := (time.Now().Unix()-songStartUnix)*1000 - pausedTotalMs
	if pausedAtUnix != 0 {
		ms -= (time.Now().Unix() - pausedAtUnix) * 1000
	}
	if ms < 0 {
		ms = 0
	}
	if d := DurationMs(); d > 0 && ms > d {
		ms = d
	}
	return ms
}

// pcmCounter 把写进播放器管道的字节数记到进度里
type pcmCounter struct{ w io.Writer }

func (c *pcmCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	AddPlayedBytes(int64(n))
	return n, err
}

// StartReporter 只有 lvgl 模式才起：每秒一行 POS，DUR/VOL 变化时才发
func StartReporter() {
	if !conf.IsLvgl {
		return
	}
	if v, err := strconv.Atoi(conf.Cfg.VolDefault); err == nil && v >= 0 && v <= 100 {
		NowVol = v
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		lastDur := int64(-1)
		lastVol := -1
		stopSent := false

		// 先把手上的状态发一遍，外壳不用等下一轮
		app.SendLocal("VOL " + strconv.Itoa(NowVol))
		lastVol = NowVol

		for range ticker.C {
			// 停止：只在第一次发 POS 0 归零，之后静默
			if IsStop {
				if !stopSent {
					app.SendLocal("POS 0")
					stopSent = true
				}
				continue
			}
			stopSent = false

			// 暂停：不发 POS，外壳靠 PAUSE 事件已经把状态切到暂停，进度天然停住。
			// 恢复后下一轮 ticker 会重新发 POS 校准，误差最多 1 秒。
			if IsPaused {
				continue
			}

			// 先发 DUR 再发 POS：外壳拿到位置时已经知道总长，不会闪一帧不确定态
			if dur := DurationMs(); dur != lastDur {
				app.SendLocal("DUR " + strconv.FormatInt(dur, 10))
				lastDur = dur
			}
			app.SendLocal("POS " + strconv.FormatInt(PlayedMs(), 10))

			if NowVol != lastVol {
				app.SendLocal("VOL " + strconv.Itoa(NowVol))
				lastVol = NowVol
			}
		}
	}()
}

// setNowVol 记录音量；lvgl 模式下顺手回显给外壳
func setNowVol(v int) {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	NowVol = v
	if conf.IsLvgl {
		app.SendLocal("VOL " + strconv.Itoa(v))
	}
}

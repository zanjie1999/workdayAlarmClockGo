/*
 * 下一次唤醒时间
 * Android 壳只负责按这个时间把 CPU 叫醒，醒来跑一次 timeJob()
 */

package alarm

import (
	"fmt"
	"strconv"
	"sync"
	"time"
	"workdayAlarmClock/conf"
)

var (
	mu       sync.Mutex
	lastSent int64
)

// SendNextWake 把下一次需要唤醒的绝对毫秒时间戳打印给壳
func SendNextWake() {
	if !conf.IsApp {
		return
	}
	ms := nextWakeAt(time.Now())
	mu.Lock()
	if ms == lastSent {
		mu.Unlock()
		return
	}
	lastSent = ms
	mu.Unlock()
	fmt.Println("NEXTWAKE", ms)
}

// nextWakeAt 返回下一次应该唤醒的时间戳（毫秒）
//
// 只排这些：
//   - 每个闹钟 hh:mm 的下一次，不看工作日/休息日，醒来由 timeJob() 判
//   - 00:00 换日（WorkDayApi）和 22:33 检查更新
//
// 天气不排，Android 亮屏时自己会按小时请求
func nextWakeAt(now time.Time) int64 {
	next := at(now, 0, 0)
	if t := at(now, 22, 33); t.Before(next) {
		next = t
	}
	for hhmm := range conf.Cfg.Alarm {
		if len(hhmm) != 4 {
			continue
		}
		h, errH := strconv.Atoi(hhmm[:2])
		m, errM := strconv.Atoi(hhmm[2:])
		if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			continue
		}
		if t := at(now, h, m); t.Before(next) {
			next = t
		}
	}
	return next.UnixMilli()
}

// at 取 now 之后最近的一个 h:m，过了就顺延到明天
func at(now time.Time, h, m int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

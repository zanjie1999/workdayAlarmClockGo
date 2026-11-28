//go:build linux && cgo && alsa && !app

package xiaozhi

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 厂商四麦前端服务：它自己把麦克风数据喂给 libvoice_trigger（唤醒词"小度小度"），
// 每次命中就把 /usr/data/wakeup_count 加一。
//
// 唤醒事件为什么先读这个文件、而不是用音频回调里那两个整数：回调签名和 samples
// 语义已经反汇编确认过（samples 是样本数），但第 3、4 个 int 到底是角度、VAD 还是
// 唤醒标志还没确认。计数文件是厂商自己写的，语义无歧义，先用它保证唤醒能用；
// 每次命中会顺手把最近一次回调的两个整数打进日志，对一次就能认出唤醒标志，
// 认出来之后这里就可以撤掉，少一个跨进程文件依赖。
const (
	frontEndBinary   = "/usr/bin/four_mic_front_end_alg_server"
	wakeupCountFile  = "/usr/data/wakeup_count"
	wakePollInterval = 200 * time.Millisecond
	// 服务刚起来那几秒会自己写计数/清计数，等稳定了再取基线，免得开机就误唤醒
	wakeWarmupDelay = 2 * time.Second
)

// wakeWatcher 唤醒监听。只在有厂商前端的设备上才起得来。
type wakeWatcher struct {
	log    func(string)
	onWake func()

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// frontEndProc 是我们自己拉起来的四麦前端进程；本来就在跑的话为 nil。
var frontEndProc struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

// rnsWakeAvailable 这台机器有没有可用的唤醒链路。
func rnsWakeAvailable() bool {
	if _, err := os.Stat(frontEndBinary); err != nil {
		return false
	}
	if _, err := os.Stat("/dev/mic"); err != nil {
		return false
	}
	return true
}

func startWakeWatcher(logf func(string), onWake func()) *wakeWatcher {
	if logf == nil {
		logf = func(string) {}
	}
	w := &wakeWatcher{log: logf, onWake: onWake, stop: make(chan struct{}), done: make(chan struct{})}
	if err := ensureFrontEndServer(logf); err != nil {
		logf("启动四麦前端失败：" + err.Error())
	}
	go w.loop()
	return w
}

// ensureFrontEndServer 没在跑就把四麦前端拉起来（采集和唤醒共用这一个进程）。
func ensureFrontEndServer(logf func(string)) error {
	if processRunning(frontEndBinary) {
		if logf != nil {
			logf("四麦前端已在运行，直接复用")
		}
		return nil
	}
	frontEndProc.mu.Lock()
	defer frontEndProc.mu.Unlock()
	if frontEndProc.cmd != nil {
		return nil
	}
	cmd := exec.Command(frontEndBinary)
	if err := cmd.Start(); err != nil {
		return err
	}
	frontEndProc.cmd = cmd
	if logf != nil {
		logf("已启动四麦前端 pid=" + strconv.Itoa(cmd.Process.Pid))
	}
	// 必须 Wait，否则子进程退出后会留僵尸
	go func() { _ = cmd.Wait() }()
	return nil
}

// stopFrontEndServer 关掉我们自己拉起来的四麦前端。
func stopFrontEndServer() {
	frontEndProc.mu.Lock()
	defer frontEndProc.mu.Unlock()
	if frontEndProc.cmd != nil && frontEndProc.cmd.Process != nil {
		_ = frontEndProc.cmd.Process.Kill()
		frontEndProc.cmd = nil
	}
}

func (w *wakeWatcher) loop() {
	defer close(w.done)

	// 服务启动那几秒会自己写一次计数，等它稳定后再取基线
	time.Sleep(wakeWarmupDelay)
	last := readWakeCount()
	w.log("唤醒计数基线 " + strconv.FormatInt(last, 10))

	ticker := time.NewTicker(wakePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			cur := readWakeCount()
			if cur > last {
				last = cur
				// 顺手带上音频回调最近一次的整数，用来确认哪个是唤醒标志
				a, b := lastCallbackInts()
				w.log("检测到唤醒（最近回调 a=" + strconv.Itoa(a) + " b=" + strconv.Itoa(b) + "）")
				if w.onWake != nil {
					w.onWake()
				}
			} else if cur < last {
				// 服务重启过会把计数清零，跟着回落
				last = cur
			}
		}
	}
}

// Stop 停监听，并收拾我们自己拉起来的服务。
func (w *wakeWatcher) Stop() {
	w.once.Do(func() { close(w.stop) })
	<-w.done
}

// readWakeCount 读唤醒计数，读不到按 0 算。
func readWakeCount() int64 {
	raw, err := os.ReadFile(wakeupCountFile)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// processRunning 扫 /proc 找同名进程，避免依赖 pgrep 这种不一定有的工具。
func processRunning(binary string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		cmdline := strings.ReplaceAll(string(raw), "\x00", " ")
		if strings.Contains(cmdline, binary) {
			return true
		}
	}
	return false
}

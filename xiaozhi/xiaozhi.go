//go:build linux && cgo && alsa && !app

// Package xiaozhi 把 core 里的协议逻辑接到真实设备上：
// 管连接生命周期、OTA 配对、麦克风与喇叭、Opus 编解码。
//
// 只在 linux + cgo 且不是 Android App 形态时编译；其他平台由 stub.go 提供空实现。
package xiaozhi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/player"
	"workdayAlarmClock/xiaozhi/core"
)

var (
	mu      sync.Mutex
	status  = Status{State: "idle"}
	cancel  context.CancelFunc
	running bool
	// trigger 手动开始一轮对话的信号，唤醒词命中发的也是这个
	trigger chan struct{}
	// sessionActive 当前是否有一轮对话在跑，唤醒进来时据此决定是"开始"还是"打断"
	sessionActive atomic.Bool
)

// Available 当前编译形态下功能是否可用（stub 里恒为 false）。
func Available() bool { return true }

// Running 是否正在运行。
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return running
}

// Current 取状态快照。
func Current() Status {
	mu.Lock()
	defer mu.Unlock()
	return status
}

func update(fn func(*Status)) {
	mu.Lock()
	defer mu.Unlock()
	fn(&status)
	status.UpdatedAt = time.Now().Unix()
}

func setState(s core.State) {
	update(func(st *Status) {
		st.State = s.String()
		if s == core.StateIdle {
			st.ActivationCode = ""
			st.ActivationMessage = ""
		}
	})
}

// Start 启动后台会话（已经在跑就先停）。
// 这个函数只负责拉起线程，真正的连接结果看 Current()。
func Start(opts Options) error {
	Stop()

	// 设备身份必须稳定，否则每次都会被服务端当成新设备。
	// 只覆盖身份字段，其余仍用调用方传进来的配置。
	conf.EnsureXiaozhiIdentity()
	opts.Cfg = conf.NormalizeXiaozhi(opts.Cfg)
	opts.Cfg.DeviceMac = conf.Cfg.Xiaozhi.DeviceMac
	opts.Cfg.ClientId = conf.Cfg.Xiaozhi.ClientId

	ctx, cancelFn := context.WithCancel(context.Background())
	trig := make(chan struct{}, 1)

	mu.Lock()
	cancel = cancelFn
	trigger = trig
	running = true
	mu.Unlock()

	update(func(st *Status) {
		*st = Status{State: "connecting", Running: true, UpdatedAt: time.Now().Unix()}
	})

	go func() {
		defer func() {
			mu.Lock()
			running = false
			mu.Unlock()
			update(func(st *Status) {
				st.Running = false
				if st.State != "error" {
					st.State = "idle"
				}
			})
		}()
		runLoop(ctx, opts, trig)
	}()
	return nil
}

// Trigger 立刻开始一轮对话。唤醒词命中、页面上点"开始对话"走的都是这里。
// 后台没在跑（没启用）时返回 false，调用方应该先 Start。
func Trigger() bool {
	mu.Lock()
	ch := trigger
	isRunning := running
	mu.Unlock()
	if !isRunning || ch == nil {
		return false
	}
	select {
	case ch <- struct{}{}:
	default:
		// 已经有一个待处理的触发了，不重复排队
	}
	return true
}

// Stop 停止后台会话，可重复调用。
func Stop() {
	mu.Lock()
	cancelFn := cancel
	cancel = nil
	wasRunning := running
	running = false
	mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}
	// 四麦前端是我们拉起来的就一起关掉，别让它在后台吃 60% 的 CPU
	stopFrontEndServer()
	if wasRunning {
		update(func(st *Status) {
			st.State = "idle"
			st.ActivationCode = ""
			st.ActivationMessage = ""
			st.Running = false
		})
	}
}

// runLoop 后台主循环。
//
// 开了语音唤醒：常驻等唤醒，命中才开始连（和 Android 的待机监听一致），
// 一轮说完回到等待，不自动重连。
// 没开唤醒（或这台机器没有唤醒引擎）：拉起后直接连，出错按退避重试。
func runLoop(ctx context.Context, opts Options, trig chan struct{}) {
	logf := func(s string) { log.Println("小智：", s) }

	// 播报中再喊唤醒词 → 打断；待机时喊 → 开始新一轮
	interrupt := make(chan struct{}, 1)

	waitWake := false
	if opts.Cfg.WakeEnabled {
		if rnsWakeAvailable() {
			wake := startWakeWatcher(logf, func() {
				if sessionActive.Load() {
					signalInterrupt(ctx, interrupt)
					return
				}
				signalTrigger(ctx, trig)
			})
			defer wake.Stop()
			waitWake = true
			logf("语音唤醒已就绪，等待「小度小度」")
		} else {
			logf("这台设备没有唤醒引擎，改为直接连接")
		}
	}
	if !waitWake {
		signalTrigger(ctx, trig)
	}

	backoff := 3 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		// 待机：等唤醒或手动触发
		if waitWake {
			select {
			case <-ctx.Done():
				return
			case <-trig:
			}
			if ctx.Err() != nil {
				return
			}
		}

		endpoint, err := resolveEndpoint(ctx, opts.Cfg, opts.Version, opts.BoardName)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			update(func(st *Status) { st.Error = err.Error() })
			log.Println("小智：", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 3 * time.Second

		err = runOnce(ctx, opts, endpoint, interrupt)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			// 正常收场（服务端 goodbye 或静默超时）
			update(func(st *Status) {
				st.State = "idle"
				st.ActivationCode = ""
				st.ActivationMessage = ""
			})
			if !waitWake {
				return
			}
			continue
		}

		update(func(st *Status) { st.Error = err.Error() })
		log.Println("小智会话结束：", err)
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// signalInterrupt 让正在进行的这轮把话停掉、重新听。
func signalInterrupt(ctx context.Context, ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
	default:
	}
}

// signalTrigger 往触发通道丢一次信号，正在跑就不重复排队。
func signalTrigger(ctx context.Context, ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
	default:
	}
}

// resolveEndpoint 决定这次连哪儿：手动模式直接连，自动模式问 OTA（未绑定就等配对）。
func resolveEndpoint(ctx context.Context, cfg conf.XiaozhiSettings, version, board string) (core.Endpoint, error) {
	if strings.TrimSpace(board) == "" {
		board = boardName()
	}
	client := &core.OtaClient{
		OtaURL:     cfg.OtaUrl,
		DeviceMac:  cfg.DeviceMac,
		ClientID:   cfg.ClientId,
		AppVersion: version,
		BoardName:  board,
		Log:        func(s string) { log.Println("小智 OTA：", s) },
	}
	body := client.Body()

	res, err := client.Request(ctx, body)
	if err != nil {
		return core.Endpoint{}, err
	}
	if res.WsURL != "" {
		return core.Endpoint{URL: res.WsURL, Token: res.Token}, nil
	}
	if res.Activation == nil {
		return core.Endpoint{}, errors.New("OTA 没给连接地址，也没给配对码")
	}

	// 未绑定：把配对码抛给页面，然后开始轮询
	update(func(st *Status) {
		st.ActivationCode = res.Activation.Code
		st.ActivationMessage = res.Activation.Message
		st.State = "pairing"
	})
	log.Println("小智：等待配对，配对码", res.Activation.Code)

	res, err = client.PollActivation(ctx, body, 3*time.Second, func(code, msg string) {
		update(func(st *Status) {
			st.ActivationCode = code
			st.ActivationMessage = msg
		})
		log.Println("小智：配对码更新为", code)
	})
	if err != nil {
		return core.Endpoint{}, err
	}
	if res.WsURL == "" {
		return core.Endpoint{}, errors.New("配对完成但没拿到连接地址")
	}
	update(func(st *Status) {
		st.ActivationCode = ""
		st.ActivationMessage = ""
	})
	return core.Endpoint{URL: res.WsURL, Token: res.Token}, nil
}

// CheckActivation 主动问一次服务端：这台设备绑定了没有。
// 已绑定返回 Activated；没绑定返回配对码，用户拿它去网页输入。
func CheckActivation(ctx context.Context, opts Options) (ActivationResult, error) {
	conf.EnsureXiaozhiIdentity()
	cfg := conf.NormalizeXiaozhi(opts.Cfg)
	cfg.DeviceMac = conf.Cfg.Xiaozhi.DeviceMac
	cfg.ClientId = conf.Cfg.Xiaozhi.ClientId

	board := opts.BoardName
	if strings.TrimSpace(board) == "" {
		board = boardName()
	}
	client := &core.OtaClient{
		OtaURL:     cfg.OtaUrl,
		DeviceMac:  cfg.DeviceMac,
		ClientID:   cfg.ClientId,
		AppVersion: opts.Version,
		BoardName:  board,
	}
	res, err := client.Request(ctx, client.Body())
	if err != nil {
		return ActivationResult{}, err
	}
	if res.WsURL != "" {
		return ActivationResult{Activated: true}, nil
	}
	if res.Activation != nil {
		return ActivationResult{Code: res.Activation.Code, Message: res.Activation.Message}, nil
	}
	return ActivationResult{}, errors.New("OTA 没给连接地址，也没给配对码")
}

// runOnce 跑一次完整会话，期间按配置暂停音乐。
func runOnce(ctx context.Context, opts Options, endpoint core.Endpoint, interrupt <-chan struct{}) error {
	audio, err := newAudioIO(opts.ALSAOutput, opts.ALSAInput, func(s string) { log.Println("小智音频：", s) })
	if err != nil {
		return err
	}
	defer audio.Close()

	sessionActive.Store(true)
	defer sessionActive.Store(false)

	update(func(st *Status) { st.Backend = audio.Backend() })

	paused := false
	if opts.Cfg.PauseMedia && !player.IsStop && !player.IsPaused {
		player.Pause()
		paused = true
	}
	defer func() {
		// 只恢复我们自己暂停的；本来就没播的不能让它突然唱起来
		if paused && !player.IsStop {
			player.Resume()
		}
	}()

	deps := core.Deps{
		Audio:   audio,
		Tools:   deviceTools{},
		Version: opts.Version,
		Log:     func(s string) { log.Println("小智：", s) },
		OnState: setState,
		OnText: func(kind, text string) {
			update(func(st *Status) {
				st.LastKind = kind
				st.LastText = text
			})
			echoText(kind, text)
		},
		NewEncoder: func() (core.Encoder, error) {
			return newOpusEncoder(core.UploadSampleRate, core.UploadChannels, 16000, 5)
		},
		NewDecoder: func(rate int) (core.Decoder, error) {
			return newOpusDecoder(rate)
		},
	}

	return core.Run(ctx, core.Options{
		Endpoint:         endpoint,
		Mode:             conf.XiaozhiMode,
		AutoContinue:     opts.Cfg.AutoContinue,
		SilenceTimeoutMs: opts.Cfg.SilenceTimeoutMs,
		DeviceID:         opts.Cfg.DeviceMac,
		ClientID:         opts.Cfg.ClientId,
		Interrupt:        interrupt,
	}, deps)
}

// echoText 把对话推到外壳的 ECHO 行上显示。
// 识别到的话前面挂"你："；小智的回复服务端是一段一段发的，来一段就刷一段，
// 不拼接、不加前缀——外壳那行只有一条，刷最新那段就对了。
func echoText(kind, text string) {
	line := strings.TrimSpace(text)
	if line == "" {
		return
	}
	switch kind {
	case "stt":
		app.Send("ECHO 你：" + line)
	case "tts":
		app.Send("ECHO " + line)
	}
}

// boardName 上报给服务端的板子名，优先读 /etc/hostname。
func boardName() string {
	raw, err := os.ReadFile("/etc/hostname")
	if err == nil {
		if s := strings.TrimSpace(string(raw)); s != "" {
			return s
		}
	}
	return "generic"
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// audioIO 把采集和播放拼成 core.AudioIO。
type audioIO struct {
	sink     *playbackSink
	src      *captureSource
	deviceIn string
	srcMu    sync.Mutex
	backend  string
	// rns 当前采集是不是走的厂商四麦前端
	rns bool
}

func newAudioIO(deviceOut, deviceIn string, logf func(string)) (*audioIO, error) {
	if deviceOut == "" {
		deviceOut = "default"
	}
	if deviceIn == "" {
		deviceIn = "default"
	}
	return &audioIO{
		sink:     newPlaybackSink(deviceOut, logf),
		deviceIn: deviceIn,
		backend:  "alsa",
	}, nil
}

// Backend 报告采集后端名字，给页面显示。
func (a *audioIO) Backend() string { return a.backend }

func (a *audioIO) StartCapture(onFrame func([]int16)) error {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	if a.rns {
		return nil
	}
	if a.src != nil {
		return nil
	}

	logf := func(s string) { log.Println("小智音频：", s) }

	// 有厂商四麦前端就用它：波束、降噪、AEC 都在那边做完了，音质比裸麦好一截
	if rnsAvailable() {
		if err := startRNSCapture(onFrame, logf); err != nil {
			logf("接四麦前端失败，退回 ALSA：" + err.Error())
		} else {
			a.rns = true
			a.backend = "rns(4mic)"
			return nil
		}
	}

	src, err := openCapture(a.deviceIn, logf)
	if err != nil {
		return err
	}
	a.src = src
	a.backend = fmt.Sprintf("%s(%dch)", src.backend, src.ch)
	src.start(onFrame)
	return nil
}

func (a *audioIO) StopCapture() {
	a.srcMu.Lock()
	defer a.srcMu.Unlock()
	if a.rns {
		stopRNSCapture()
		a.rns = false
		return
	}
	if a.src == nil {
		return
	}
	a.src.close()
	a.src = nil
}

func (a *audioIO) Play(pcm []int16, rate int) error { return a.sink.Play(pcm, rate) }
func (a *audioIO) Flush()                           { a.sink.Flush() }
func (a *audioIO) Pending() int                     { return a.sink.Pending() }

// Close 会话结束时收尾。
func (a *audioIO) Close() {
	a.StopCapture()
	a.sink.Close()
}

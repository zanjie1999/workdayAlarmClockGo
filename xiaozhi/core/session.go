package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Encoder 把 16k/1ch 的 PCM 帧编成 Opus 包。
type Encoder interface {
	Encode(pcm []int16) ([]byte, error)
	Close()
}

// Decoder 把 Opus 包解成 PCM 采样。采样率由服务端握手时下发。
type Decoder interface {
	Decode(pkt []byte) ([]int16, error)
	Close()
}

// Transport 一条双向连接，生产环境是 WSTransport，测试里用假的。
type Transport interface {
	SendText([]byte) error
	SendBinary([]byte) error
	Receive() (Frame, error)
	Close() error
}

// AudioIO 采集与播放。实现放在带 cgo 的 xiaozhi 包里，这里只依赖接口。
type AudioIO interface {
	// StartCapture 开始采集，每凑满 FrameSamples 个采样回调一次。
	// 回调是阻塞的：实现方要自己保证背压，不要无限堆队列。
	StartCapture(onFrame func([]int16)) error
	// StopCapture 停止采集，可重复调用
	StopCapture()
	// Play 把一段 PCM 放进播放队列（非阻塞）。rate 是这段音频的采样率。
	Play(pcm []int16, rate int) error
	// Flush 丢掉还没播完的内容
	Flush()
	// Pending 队列里还没播完的包数，用来判断"这一轮说完了"
	Pending() int
}

// Tool 设备侧 MCP 暴露的一个工具。
type Tool struct {
	Name        string
	Description string
	// InputSchema JSON Schema 对象，nil 表示无参数
	InputSchema map[string]any
}

// ToolResult 一次工具调用的结果。
type ToolResult struct {
	// Text 给模型看的一句话
	Text string
	// EndTurn 执行完这条调用后这轮对话就结束，不再续听。
	// 点歌这类"开始了就别再听"的命令用它。
	EndTurn bool
}

// ToolProvider 提供设备能做的事（播放控制、状态查询等）。
type ToolProvider interface {
	Tools() []Tool
	Call(name string, args json.RawMessage) (ToolResult, error)
}

// Endpoint 一次会话要连的地址。
type Endpoint struct {
	URL   string
	Token string
}

// Options 一次会话的可调参数。
type Options struct {
	Endpoint         Endpoint
	Mode             string
	AutoContinue     bool
	SilenceTimeoutMs int
	// HelloTimeout 建连后等 hello 的上限，0 用 10s
	HelloTimeout time.Duration
	// DeviceID 设备身份，连上时放在 Device-Id 头里，服务端按它认设备
	DeviceID string
	// ClientID 客户端身份，放在 Client-Id 头里
	ClientID string
	// Interrupt 播报中收到唤醒（或按键打断）时发信号；nil 表示不支持打断
	Interrupt <-chan struct{}
}

// Deps 会话的外部依赖，全部可替换，方便单测。
type Deps struct {
	// NewTransport 拨号，nil 用 DialWS
	NewTransport func(ctx context.Context, url string, header http.Header) (Transport, error)
	// NewEncoder 建上行编码器（16k 单声道），nil 表示不支持上行
	NewEncoder func() (Encoder, error)
	// NewDecoder 按服务端下发的采样率建下行解码器
	NewDecoder func(sampleRate int) (Decoder, error)
	// Audio 采集与播放
	Audio AudioIO
	// Tools 设备侧工具，nil 表示不暴露任何工具
	Tools ToolProvider
	// Version 上报的固件版本
	Version string
	// Log 日志
	Log func(string)
	// OnState 状态变化回调
	OnState func(State)
	// OnText 文本回调：kind 取 stt / llm / tts
	OnText func(kind, text string)
}

const (
	// defaultHelloTimeout 建连后等 hello 的上限
	defaultHelloTimeout = 10 * time.Second
	// drainPollInterval 判断播放播完的轮询间隔
	drainPollInterval = 100 * time.Millisecond
	// drainSettle 队列空了之后再稳定这么久才算真的播完（避免每包之间的空隙误判）
	drainSettle = 300 * time.Millisecond
	// drainTimeout 兜底：这么久还没播完就强行认为播完，避免卡在说话状态
	drainTimeout = 60 * time.Second
	// mcpLegacyVersion 只声明旧版 MCP，网关就不会发 server/discover
	mcpLegacyVersion = "2024-11-05"
)

// Run 跑一次完整会话，阻塞到会话结束。
//
// 结束原因分三种：ctx 取消（返回 nil）、服务端 goodbye（返回 nil）、
// 连接异常或握手失败（返回 error，调用方决定要不要重连）。
func Run(ctx context.Context, opt Options, deps Deps) error {
	if deps.Log == nil {
		deps.Log = func(string) {}
	}
	if deps.Audio == nil {
		return errors.New("没有可用的音频设备")
	}
	if deps.NewEncoder == nil {
		return errors.New("没有可用的 Opus 编码器")
	}

	timeout := opt.HelloTimeout
	if timeout <= 0 {
		timeout = defaultHelloTimeout
	}
	mode := strings.TrimSpace(opt.Mode)
	if mode == "" {
		mode = "auto"
	}

	header := http.Header{}
	if opt.Endpoint.Token != "" {
		header.Set("Authorization", "Bearer "+opt.Endpoint.Token)
	}
	header.Set("Protocol-Version", fmt.Sprint(ProtocolVersion))
	if opt.DeviceID != "" {
		header.Set("Device-Id", opt.DeviceID)
	}
	if opt.ClientID != "" {
		header.Set("Client-Id", opt.ClientID)
	}

	newTransport := deps.NewTransport
	if newTransport == nil {
		newTransport = func(ctx context.Context, url string, h http.Header) (Transport, error) {
			return DialWS(ctx, url, h)
		}
	}

	s := &session{
		ctx:      ctx,
		opt:      opt,
		mode:     mode,
		deps:     deps,
		machine:  NewMachine(opt.AutoContinue, opt.SilenceTimeoutMs),
		sigCh:    make(chan Signal, 32),
		doneCh:   make(chan struct{}),
		helloTO:  timeout,
		state:    StateIdle,
		textSink: deps.OnText,
	}

	// 先进入连接态，再拨号：状态机要求 Start 之后才认 TransportOpen
	s.dispatch(SigStart{})

	transport, err := newTransport(ctx, opt.Endpoint.URL, header)
	if err != nil {
		return fmt.Errorf("连接失败：%w", err)
	}
	s.transport = transport
	defer s.cleanup()

	encoder, err := deps.NewEncoder()
	if err != nil {
		return fmt.Errorf("编码器初始化失败：%w", err)
	}
	s.encoder = encoder

	// 建连成功 → 发 hello → 起读线程
	s.dispatch(SigTransportOpen{})
	if err := s.transport.SendText(HelloJSON(s.helloFeatures())); err != nil {
		return fmt.Errorf("发送 hello 失败：%w", err)
	}
	s.logf("已连接 %s，等待 hello", opt.Endpoint.URL)
	s.startHelloTimer()
	go s.readLoop()
	go func() {
		select {
		case <-ctx.Done():
			s.dispatch(SigStop{Reason: "会话被取消"})
		case <-s.doneCh:
		}
	}()

	// 控制器循环：所有状态迁移都在这里串行执行，副作用也就天然有序
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig := <-s.sigCh:
			if stop, runErr := s.handle(sig); stop {
				return runErr
			}
		case <-opt.Interrupt:
			// 播报中又喊了唤醒词：丢掉正在说的话，立刻重新听
			s.logf("被打断，重新开始听")
			if stop, runErr := s.handle(SigInterrupt{}); stop {
				return runErr
			}
		}
	}
}

type session struct {
	ctx  context.Context
	opt  Options
	mode string
	deps Deps

	machine *Machine
	state   State

	transport Transport
	encoder   Encoder
	decoder   Decoder

	sigCh  chan Signal
	doneCh chan struct{}
	// closeOnce 保证 doneCh 只关一次
	closeOnce sync.Once

	helloTO time.Duration

	helloTimer   *time.Timer
	silenceTimer *time.Timer
	drainTimer   *time.Timer
	drainSince   time.Time
	draining     bool

	capturing bool
	textSink  func(kind, text string)
	// rate 服务端下发的播放采样率，0 表示还没握手。
	// 读线程和控制器线程都会碰，用原子量避免竞争。
	rate atomic.Int32
	// lastErr 记录结束原因，供 return 使用
	lastErr error
	// stopAfterTurn 这轮播完就收场，不再续听（点歌类命令用）
	stopAfterTurn bool
}

func (s *session) logf(format string, args ...any) {
	s.deps.Log(fmt.Sprintf(format, args...))
}

func (s *session) helloFeatures() map[string]bool {
	return mcpFeatures()
}

// dispatch 把事件塞进控制器。满了说明控制器卡住，此时丢事件比死锁好。
func (s *session) dispatch(sig Signal) {
	select {
	case s.sigCh <- sig:
	case <-s.ctx.Done():
	default:
		s.logf("事件队列已满，丢弃 %s", sig.signalName())
	}
}

// readLoop 收服务端的帧：文本交给协议解析，二进制交给解码器。
func (s *session) readLoop() {
	for {
		frame, err := s.transport.Receive()
		if err != nil {
			s.dispatch(SigTransportClosed{Reason: describeNetErr(err)})
			return
		}
		if frame.Binary {
			s.onAudio(frame.Data)
			continue
		}
		s.onText(frame.Data)
	}
}

func (s *session) onText(raw []byte) {
	ev, err := ParseServerEvent(raw)
	if err != nil {
		s.logf("无法解析的服务端消息：%s", trimForLog(raw))
		return
	}
	switch e := ev.(type) {
	case EventHello:
		s.logf("握手完成 session_id=%s 采样率=%d", e.SessionID, e.SampleRate)
		s.stopHelloTimer()
		if s.decoder == nil && s.deps.NewDecoder != nil {
			dec, derr := s.deps.NewDecoder(e.SampleRate)
			if derr != nil {
				s.dispatch(SigFatal{Message: "解码器初始化失败：" + derr.Error()})
				return
			}
			s.decoder = dec
		}
		s.dispatch(SigHello{SampleRate: e.SampleRate})
	case EventStt:
		if e.Text != "" {
			s.emit("stt", e.Text)
		}
		s.dispatch(SigStt{Text: e.Text})
	case EventTts:
		switch e.State {
		case "start":
			s.dispatch(SigTtsStart{})
		case "stop":
			s.dispatch(SigTtsStop{})
		case "sentence_start":
			// 回复是一段一段来的，来一段就交出去一段，不拼接
			if e.Text != "" {
				s.emit("tts", e.Text)
			}
			s.dispatch(SigTtsSentence{Text: e.Text})
		default:
			// sentence_end 之类不用特殊处理，等 tts stop 再排空
		}
	case EventLlm:
		if e.Text != "" {
			s.emit("llm", e.Text)
		}
	case EventAlert:
		msg := e.Message
		if msg == "" {
			msg = e.Status
		}
		s.logf("服务端提示：%s", msg)
	case EventGoodbye:
		// 服务端主动收场（比如"退下"），不再重连
		s.dispatch(SigStop{Reason: "已退出对话"})
	case EventMcp:
		s.handleMcp(e)
	case EventUnknown:
		s.logf("忽略消息类型 %s", e.Type)
	}
}

// onAudio 下行 Opus 包：解码后直接入播放队列。
func (s *session) onAudio(pkt []byte) {
	if s.decoder == nil {
		return
	}
	pcm, err := s.decoder.Decode(pkt)
	if err != nil || len(pcm) == 0 {
		return
	}
	if err := s.deps.Audio.Play(pcm, s.downloadRate()); err != nil {
		s.logf("播放失败：%v", err)
	}
}

func (s *session) downloadRate() int {
	if v := s.rate.Load(); v > 0 {
		return int(v)
	}
	return DefaultDownloadRate
}

// handle 执行一次迁移。返回 stop=true 表示会话结束。
func (s *session) handle(sig Signal) (bool, error) {
	// 点歌类命令执行完，这轮播完就收场，不再续听
	if _, ok := sig.(SigPlaybackDrained); ok && s.stopAfterTurn {
		s.stopAfterTurn = false
		sig = SigStop{Reason: "播放命令已执行"}
	}

	// 断开/致命错误要先记下原因，迁移里只负责发副作用
	switch e := sig.(type) {
	case SigTransportClosed:
		s.lastErr = errors.New(describeNetErr(errors.New(e.Reason)))
	case SigFatal:
		s.lastErr = errors.New(e.Message)
	case SigHello:
		s.rate.Store(int32(e.SampleRate))
	}

	tr := s.machine.Accept(sig)
	if tr == nil {
		// 状态机认为没意义，但连接没了或出错了还是得结束，否则会一直挂着
		switch sig.(type) {
		case SigTransportClosed, SigFatal:
			s.closeOnce.Do(func() { close(s.doneCh) })
			if s.lastErr == nil {
				s.lastErr = errors.New("连接已断开")
			}
			return true, s.lastErr
		}
		return false, nil
	}
	s.logf("状态 %s -> %s", tr.From, tr.To)
	for _, eff := range tr.Effects {
		s.apply(eff)
	}
	s.setState(tr.To)

	// 播报文本结束 → 开始盯播放队列，队列空了才叫"这一轮说完了"
	if _, ok := sig.(SigTtsStop); ok && tr.To == StateSpeaking {
		s.startDrainWatcher()
	}

	if tr.To == StateIdle || tr.To == StateError {
		s.closeOnce.Do(func() { close(s.doneCh) })
		if tr.To == StateError {
			if s.lastErr == nil {
				s.lastErr = errors.New("会话异常结束")
			}
			return true, s.lastErr
		}
		return true, nil
	}
	return false, nil
}

func (s *session) apply(eff Effect) {
	switch e := eff.(type) {
	case EffectStartCapture:
		s.startCapture()
	case EffectStopCapture:
		s.stopCapture()
	case EffectSendListenStart:
		s.sendText(ListenStartJSON(s.mode))
	case EffectSendListenStop:
		s.sendText(ListenStopJSON())
	case EffectSendAbort:
		s.sendText(AbortJSON(AbortReasonWakeWord))
	case EffectFlushPlayback:
		s.deps.Audio.Flush()
		s.stopDrain()
	case EffectCancelSilence:
		s.stopSilenceTimer()
	case EffectScheduleSilence:
		s.startSilenceTimer(e.TimeoutMs)
	case EffectLog:
		s.logf("%s", e.Message)
	}
}

func (s *session) startCapture() {
	if s.capturing || s.encoder == nil {
		return
	}
	err := s.deps.Audio.StartCapture(func(pcm []int16) {
		if len(pcm) != FrameSamples {
			return
		}
		pkt, encErr := s.encoder.Encode(pcm)
		if encErr != nil || len(pkt) == 0 {
			return
		}
		if sendErr := s.transport.SendBinary(pkt); sendErr != nil {
			s.dispatch(SigTransportClosed{Reason: "上行发送失败：" + sendErr.Error()})
		}
	})
	if err != nil {
		s.logf("启动录音失败：%v", err)
		s.dispatch(SigFatal{Message: "启动麦克风失败：" + err.Error()})
		return
	}
	s.capturing = true
}

func (s *session) stopCapture() {
	if !s.capturing {
		return
	}
	s.deps.Audio.StopCapture()
	s.capturing = false
}

func (s *session) sendText(payload []byte) {
	if s.transport == nil {
		return
	}
	if err := s.transport.SendText(payload); err != nil {
		s.logf("发送失败：%v", err)
	}
}

// ---- 计时器 ----

func (s *session) startHelloTimer() {
	s.stopHelloTimer()
	s.helloTimer = time.AfterFunc(s.helloTO, func() { s.dispatch(SigHelloTimeout{}) })
}

func (s *session) stopHelloTimer() {
	if s.helloTimer != nil {
		s.helloTimer.Stop()
		s.helloTimer = nil
	}
}

func (s *session) startSilenceTimer(ms int) {
	s.stopSilenceTimer()
	if ms <= 0 {
		ms = 8000
	}
	s.silenceTimer = time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
		s.dispatch(SigSilenceTimeout{})
	})
}

func (s *session) stopSilenceTimer() {
	if s.silenceTimer != nil {
		s.silenceTimer.Stop()
		s.silenceTimer = nil
	}
}

// startDrainWatcher 播报文本结束后开始盯播放队列，
// 队列空了并稳定一小会儿就认为"这一轮说完了"，驱动自动续听。
func (s *session) startDrainWatcher() {
	s.stopDrain()
	s.draining = true
	s.drainSince = time.Time{}
	start := time.Now()
	var tick func()
	tick = func() {
		if !s.draining {
			return
		}
		if time.Since(start) > drainTimeout {
			s.logf("播放超时兜底，按播放完成处理")
			s.draining = false
			s.dispatch(SigPlaybackDrained{})
			return
		}
		if s.deps.Audio.Pending() <= 0 {
			if s.drainSince.IsZero() {
				s.drainSince = time.Now()
			} else if time.Since(s.drainSince) >= drainSettle {
				s.draining = false
				s.dispatch(SigPlaybackDrained{})
				return
			}
		} else {
			s.drainSince = time.Time{}
		}
		s.drainTimer = time.AfterFunc(drainPollInterval, tick)
	}
	s.drainTimer = time.AfterFunc(drainPollInterval, tick)
}

func (s *session) stopDrain() {
	s.draining = false
	if s.drainTimer != nil {
		s.drainTimer.Stop()
		s.drainTimer = nil
	}
}

// ---- MCP ----

// handleMcp 设备侧 MCP：网关先问支持的版本/能力，再列工具、调工具。
// 回包形状按每条请求的 params._meta 判断，与 Android 端一致。
func (s *session) handleMcp(ev EventMcp) {
	// 通知类消息没有 id，不能回包
	if strings.HasPrefix(ev.Method, "notifications/") {
		return
	}
	modern := isModernMcp(ev.Params)
	version := s.version()

	switch ev.Method {
	case "server/discover":
		// 新版第一步；响应本身固定按新版包装
		s.sendText(mcpReply(ev.ID, ev.SessionID, mcpDiscoverResult(), true, version))
	case "initialize":
		// 旧版第一步，新版不会发
		s.sendText(mcpReply(ev.ID, ev.SessionID, mcpInitializeResult(version), false, version))
	case "tools/list":
		s.sendText(mcpReply(ev.ID, ev.SessionID, s.mcpToolsResult(ev.Params, modern), modern, version))
	case "tools/call":
		result, endTurn := s.mcpCallResult(ev.Params)
		if endTurn {
			s.stopAfterTurn = true
		}
		s.sendText(mcpReply(ev.ID, ev.SessionID, result, modern, version))
	case "ping":
		s.sendText(mcpReply(ev.ID, ev.SessionID, map[string]any{}, modern, version))
	default:
		s.sendText(McpErrorJSON(ev.ID, -32601, "Method not found: "+ev.Method, ev.SessionID))
	}
}

// mcpToolsResult 工具清单。新版要带缓存 TTL；带 cursor 说明网关在翻页，我们不分页，第二页给空表。
func (s *session) mcpToolsResult(params json.RawMessage, modern bool) map[string]any {
	var p struct {
		Cursor string `json:"cursor"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	tools := []map[string]any{}
	if modern && p.Cursor != "" {
		return map[string]any{"tools": tools}
	}
	if s.deps.Tools != nil {
		for _, t := range s.deps.Tools.Tools() {
			def := map[string]any{"name": t.Name, "description": t.Description}
			if t.InputSchema != nil {
				def["inputSchema"] = t.InputSchema
			} else {
				def["inputSchema"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, def)
		}
	}
	result := map[string]any{"tools": tools}
	if modern {
		result["ttlMs"] = McpCacheTTLMs
		result["cacheScope"] = "private"
	}
	return result
}

// mcpCallResult 执行一次工具调用，第二个返回值表示这轮要不要收场。
func (s *session) mcpCallResult(params json.RawMessage) (map[string]any, bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	if s.deps.Tools == nil {
		return mcpTextResult("未知工具："+p.Name, true), false
	}
	result, err := s.deps.Tools.Call(p.Name, p.Arguments)
	if err != nil {
		return mcpTextResult(err.Error(), true), false
	}
	return mcpTextResult(result.Text, false), result.EndTurn
}

// ---- 收尾 ----

func (s *session) setState(st State) {
	if s.state == st {
		return
	}
	s.state = st
	if s.deps.OnState != nil {
		s.deps.OnState(st)
	}
	// 进入播报后，等文本结束时再开始盯播放队列
	if st != StateSpeaking {
		s.stopDrain()
	}
}

func (s *session) emit(kind, text string) {
	if s.textSink != nil {
		s.textSink(kind, text)
	}
}

func (s *session) version() string {
	if s.deps.Version != "" {
		return s.deps.Version
	}
	return "1.0"
}

func (s *session) cleanup() {
	s.stopHelloTimer()
	s.stopSilenceTimer()
	s.stopDrain()
	s.stopCapture()
	if s.encoder != nil {
		s.encoder.Close()
	}
	if s.decoder != nil {
		s.decoder.Close()
	}
	if s.transport != nil {
		_ = s.transport.Close()
	}
	s.deps.Audio.Flush()
}

// describeNetErr 把网络错误翻成一句能看懂的话。
func describeNetErr(err error) string {
	if err == nil {
		return "连接已断开"
	}
	msg := err.Error()
	if strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "websocket: close") {
		return "连接已断开"
	}
	return "连接异常：" + msg
}

func trimForLog(b []byte) string {
	const limit = 120
	if len(b) > limit {
		return string(b[:limit]) + "..."
	}
	return string(b)
}

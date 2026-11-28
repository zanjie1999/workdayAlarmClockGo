package core

// State 会话状态。名字与 Android 端保持一致，方便对着日志排查。
type State int

const (
	StateIdle State = iota
	StateConnecting
	StateHandshaking
	StateListening
	StateThinking
	StateSpeaking
	StateError
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateConnecting:
		return "connecting"
	case StateHandshaking:
		return "handshaking"
	case StateListening:
		return "listening"
	case StateThinking:
		return "thinking"
	case StateSpeaking:
		return "speaking"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Signal 喂给状态机的事件。与协议里的 ServerEvent 区分开：这里是"该做什么"，
// 那边是"收到了什么"，两者在 session 里做一次转换。
type Signal interface {
	signalName() string
}

type (
	// SigStart 用户/唤醒触发了新会话
	SigStart struct{}
	// SigTransportOpen WebSocket 建连成功
	SigTransportOpen struct{}
	// SigHello 收到服务端 hello
	SigHello struct{ SampleRate int }
	// SigHelloTimeout 握手超时
	SigHelloTimeout struct{}
	// SigStt 服务端识别出人声
	SigStt struct{ Text string }
	// SigTtsStart 开始播报
	SigTtsStart struct{}
	// SigTtsSentence 播报中的一句（可能紧接着 start 来）
	SigTtsSentence struct{ Text string }
	// SigTtsStop 播报文本结束
	SigTtsStop struct{}
	// SigPlaybackDrained 播放队列真的播完了
	SigPlaybackDrained struct{}
	// SigSilenceTimeout 静默超时
	SigSilenceTimeout struct{}
	// SigInterrupt 打断当前这轮（双击/唤醒词）
	SigInterrupt struct{}
	// SigStop 主动结束
	SigStop struct{ Reason string }
	// SigTransportClosed 连接断开
	SigTransportClosed struct{ Reason string }
	// SigFatal 不可恢复的错误
	SigFatal struct{ Message string }
)

func (SigStart) signalName() string           { return "start" }
func (SigTransportOpen) signalName() string   { return "transport_open" }
func (SigHello) signalName() string           { return "hello" }
func (SigHelloTimeout) signalName() string    { return "hello_timeout" }
func (SigStt) signalName() string             { return "stt" }
func (SigTtsStart) signalName() string        { return "tts_start" }
func (SigTtsSentence) signalName() string     { return "tts_sentence" }
func (SigTtsStop) signalName() string         { return "tts_stop" }
func (SigPlaybackDrained) signalName() string { return "playback_drained" }
func (SigSilenceTimeout) signalName() string  { return "silence_timeout" }
func (SigInterrupt) signalName() string       { return "interrupt" }
func (SigStop) signalName() string            { return "stop" }
func (SigTransportClosed) signalName() string { return "transport_closed" }
func (SigFatal) signalName() string           { return "fatal" }

// Effect 状态迁移后要执行的副作用，按列表顺序执行。
type Effect interface {
	effectName() string
}

type (
	// EffectStartCapture 开始上行录音
	EffectStartCapture struct{}
	// EffectStopCapture 停止上行录音
	EffectStopCapture struct{}
	// EffectSendListenStart 给服务端发 listen start
	EffectSendListenStart struct{}
	// EffectSendListenStop 给服务端发 listen stop
	EffectSendListenStop struct{}
	// EffectSendAbort 给服务端发 abort
	EffectSendAbort struct{}
	// EffectFlushPlayback 丢掉还没播完的 TTS
	EffectFlushPlayback struct{}
	// EffectCancelSilence 取消静默计时
	EffectCancelSilence struct{}
	// EffectScheduleSilence 开始静默计时
	EffectScheduleSilence struct{ TimeoutMs int }
	// EffectLog 记一条日志
	EffectLog struct{ Message string }
)

func (EffectStartCapture) effectName() string    { return "start_capture" }
func (EffectStopCapture) effectName() string     { return "stop_capture" }
func (EffectSendListenStart) effectName() string { return "send_listen_start" }
func (EffectSendListenStop) effectName() string  { return "send_listen_stop" }
func (EffectSendAbort) effectName() string       { return "send_abort" }
func (EffectFlushPlayback) effectName() string   { return "flush_playback" }
func (EffectCancelSilence) effectName() string   { return "cancel_silence" }
func (EffectScheduleSilence) effectName() string { return "schedule_silence" }
func (EffectLog) effectName() string             { return "log" }

// Transition 一次迁移的结果。
type Transition struct {
	From    State
	To      State
	Effects []Effect
}

// Machine 会话状态机。只做状态判断，不碰网络和音频，所以可以完整单测。
type Machine struct {
	// AutoContinue 说完一句后是否自动继续听
	AutoContinue bool
	// SilenceTimeoutMs 静默超时，0 时用 8000
	SilenceTimeoutMs int

	state State
}

// NewMachine 建一个空闲状态的状态机。
func NewMachine(autoContinue bool, silenceTimeoutMs int) *Machine {
	if silenceTimeoutMs <= 0 {
		silenceTimeoutMs = 8000
	}
	return &Machine{AutoContinue: autoContinue, SilenceTimeoutMs: silenceTimeoutMs, state: StateIdle}
}

// State 当前状态。
func (m *Machine) State() State { return m.state }

// Active 会话是否还在进行（空闲和错误都算结束）。
func (m *Machine) Active() bool { return m.state != StateIdle && m.state != StateError }

// Reset 回到空闲，不产生副作用。
func (m *Machine) Reset() { m.state = StateIdle }

// Accept 处理一个事件，返回 nil 表示该事件在当前状态下无意义（已忽略）。
func (m *Machine) Accept(sig Signal) *Transition {
	from := m.state
	switch ev := sig.(type) {
	case SigStart:
		if from == StateIdle || from == StateError {
			return m.transit(from, StateConnecting, EffectStopCapture{}, EffectCancelSilence{})
		}
	case SigTransportOpen:
		if from == StateConnecting {
			return m.transit(from, StateHandshaking, nil)
		}
	case SigHello:
		if from == StateHandshaking {
			return m.transit(from, StateListening,
				EffectSendListenStart{},
				EffectStartCapture{},
				EffectScheduleSilence{TimeoutMs: m.SilenceTimeoutMs},
			)
		}
	case SigHelloTimeout:
		if from == StateHandshaking {
			return m.transit(from, StateError,
				EffectStopCapture{},
				EffectCancelSilence{},
				EffectLog{Message: "握手超时：10 秒内没有收到 hello"},
			)
		}
	case SigStt:
		if from == StateListening {
			return m.transit(from, StateThinking, EffectStopCapture{}, EffectCancelSilence{})
		}
	case SigTtsStart:
		if from == StateThinking || from == StateSpeaking {
			return m.transit(from, StateSpeaking, EffectFlushPlayback{})
		}
	case SigTtsSentence:
		if from == StateThinking {
			return m.transit(from, StateSpeaking, EffectFlushPlayback{})
		}
		if from == StateSpeaking {
			return m.transit(from, StateSpeaking, nil)
		}
	case SigTtsStop:
		if from == StateThinking {
			return m.transit(from, StateSpeaking, EffectFlushPlayback{})
		}
		if from == StateSpeaking {
			return m.transit(from, StateSpeaking, nil)
		}
	case SigPlaybackDrained:
		if from == StateSpeaking {
			if m.AutoContinue {
				return m.transit(from, StateListening,
					EffectSendListenStart{},
					EffectStartCapture{},
					EffectScheduleSilence{TimeoutMs: m.SilenceTimeoutMs},
				)
			}
			return m.transit(from, StateIdle,
				EffectSendListenStop{},
				EffectStopCapture{},
				EffectCancelSilence{},
			)
		}
	case SigSilenceTimeout:
		if from == StateListening {
			return m.transit(from, StateIdle,
				EffectSendListenStop{},
				EffectStopCapture{},
				EffectCancelSilence{},
				EffectLog{Message: "静默超时，结束会话"},
			)
		}
	case SigInterrupt:
		if from == StateListening || from == StateThinking || from == StateSpeaking {
			return m.transit(from, StateListening,
				EffectFlushPlayback{},
				EffectSendAbort{},
				EffectStopCapture{},
				EffectSendListenStart{},
				EffectStartCapture{},
				EffectScheduleSilence{TimeoutMs: m.SilenceTimeoutMs},
			)
		}
	case SigStop:
		if from == StateIdle {
			return nil
		}
		return m.transit(from, StateIdle, EffectCancelSilence{}, EffectStopCapture{}, EffectSendListenStop{})
	case SigTransportClosed:
		if from == StateIdle {
			return nil
		}
		reason := ev.Reason
		if reason == "" {
			reason = "连接已断开"
		}
		return m.transit(from, StateError, EffectCancelSilence{}, EffectStopCapture{},
			EffectLog{Message: reason})
	case SigFatal:
		if from == StateIdle {
			return nil
		}
		return m.transit(from, StateError, EffectCancelSilence{}, EffectStopCapture{},
			EffectLog{Message: "错误：" + ev.Message})
	}
	return nil
}

func (m *Machine) transit(from, to State, effects ...Effect) *Transition {
	m.state = to
	return &Transition{From: from, To: to, Effects: effects}
}

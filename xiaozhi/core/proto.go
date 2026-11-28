// Package core 是小智语音助手的纯逻辑部分：协议编解码、会话状态机、OTA 与 WebSocket。
//
// 这里一行 cgo 都没有，也不带 build tag，所以能在任何平台编译和跑单测；
// 真正碰声卡和麦克风的活儿在上一层的 xiaozhi 包里，通过接口注入进来。
package core

import (
	"encoding/json"
	"strings"
)

const (
	// ProtocolVersion 小智 WebSocket 协议版本，握手时上报
	ProtocolVersion = 1
	// UploadSampleRate 上行采样率，协议按官方固件固定 16k
	UploadSampleRate = 16000
	// UploadChannels 上行声道数
	UploadChannels = 1
	// FrameDurationMs 上行帧长，服务端按时长切分，60ms 是官方取值
	FrameDurationMs = 60
	// FrameSamples 一帧的采样点数 = 16000 * 60 / 1000
	FrameSamples = UploadSampleRate * FrameDurationMs / 1000
	// DefaultDownloadRate 服务端没给 audio_params 时的回落采样率
	DefaultDownloadRate = 24000
	// AbortReasonWakeWord 唤醒词打断播报时上报的原因
	AbortReasonWakeWord = "wake_word_detected"
	// DeviceLanguage 服务端据此决定系统播报（配对码等）用哪种语言
	DeviceLanguage = "zh-CN"
)

// helloJSON 的音频参数
type audioParams struct {
	Format        string `json:"format"`
	SampleRate    int    `json:"sample_rate"`
	Channels      int    `json:"channels"`
	FrameDuration int    `json:"frame_duration"`
}

// HelloJSON 构造握手包。features 为设备能力声明（如 mcp），空则不写这个字段。
func HelloJSON(features map[string]bool) []byte {
	msg := struct {
		Type        string          `json:"type"`
		Version     int             `json:"version"`
		Transport   string          `json:"transport"`
		AudioParams audioParams     `json:"audio_params"`
		Features    map[string]bool `json:"features,omitempty"`
	}{
		Type:      "hello",
		Version:   ProtocolVersion,
		Transport: "websocket",
		AudioParams: audioParams{
			Format:        "opus",
			SampleRate:    UploadSampleRate,
			Channels:      UploadChannels,
			FrameDuration: FrameDurationMs,
		},
		Features: features,
	}
	b, _ := json.Marshal(msg)
	return b
}

// ListenStartJSON 开始一轮听写。mode 为空按 auto 处理。
func ListenStartJSON(mode string) []byte {
	if strings.TrimSpace(mode) == "" {
		mode = "auto"
	}
	msg := struct {
		SessionID string `json:"session_id"`
		Type      string `json:"type"`
		State     string `json:"state"`
		Mode      string `json:"mode"`
	}{"", "listen", "start", mode}
	b, _ := json.Marshal(msg)
	return b
}

// ListenStopJSON 结束一轮听写。
func ListenStopJSON() []byte {
	msg := struct {
		SessionID string `json:"session_id"`
		Type      string `json:"type"`
		State     string `json:"state"`
	}{"", "listen", "stop"}
	b, _ := json.Marshal(msg)
	return b
}

// ListenDetectJSON 把本地唤醒命中的文本告诉服务端（唤醒词在线识别时用）。
func ListenDetectJSON(text string) []byte {
	msg := struct {
		SessionID string `json:"session_id"`
		Type      string `json:"type"`
		State     string `json:"state"`
		Text      string `json:"text"`
	}{"", "listen", "detect", text}
	b, _ := json.Marshal(msg)
	return b
}

// AbortJSON 让服务端停掉当前这轮（丢掉已生成的回复）。
func AbortJSON(reason string) []byte {
	if reason == "" {
		reason = AbortReasonWakeWord
	}
	msg := struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}{"abort", reason}
	b, _ := json.Marshal(msg)
	return b
}

// ServerEvent 服务端下发的消息。
type ServerEvent interface {
	eventType() string
}

// EventHello 握手完成，携带本次会话的播放采样率。
type EventHello struct {
	SessionID       string
	Transport       string
	SampleRate      int
	FrameDurationMs int
}

// EventStt 语音识别结果。
type EventStt struct{ Text string }

// EventTts 语音合成进度，state 有 start / sentence_start / stop 等。
type EventTts struct {
	State string
	Text  string
}

// EventLlm 大模型文本回复（可能先单独发一个表情）。
type EventLlm struct {
	Text    string
	Emotion string
}

// EventAlert 服务端提示（配额、错误等）。
type EventAlert struct {
	Status  string
	Message string
}

// EventGoodbye 服务端主动收场（比如用户说"退下"），此时不该重连。
type EventGoodbye struct{}

// EventMcp 设备侧 MCP 请求，payload 里是 JSON-RPC。
// ID 用 RawMessage 原样保存：可能是数字也可能是字符串，回包必须一模一样带回去。
type EventMcp struct {
	ID        json.RawMessage
	Method    string
	Params    json.RawMessage
	SessionID string
}

// EventUnknown 不认识的消息类型，忽略即可。
type EventUnknown struct{ Type string }

func (EventHello) eventType() string   { return "hello" }
func (EventStt) eventType() string     { return "stt" }
func (EventTts) eventType() string     { return "tts" }
func (EventLlm) eventType() string     { return "llm" }
func (EventAlert) eventType() string   { return "alert" }
func (EventGoodbye) eventType() string { return "goodbye" }
func (EventMcp) eventType() string     { return "mcp" }
func (EventUnknown) eventType() string { return "unknown" }

// ParseServerEvent 解析一条服务端文本消息。
// 字段缺失一律用零值兜底，宁可少一条消息也不要因为服务端改字段把会话搞崩。
func ParseServerEvent(raw []byte) (ServerEvent, error) {
	var head struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		Text      string          `json:"text"`
		State     string          `json:"state"`
		Status    string          `json:"status"`
		Message   string          `json:"message"`
		Emotion   string          `json:"emotion"`
		Transport string          `json:"transport"`
		Audio     json.RawMessage `json:"audio_params"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}

	switch head.Type {
	case "hello":
		ev := EventHello{
			SessionID:       head.SessionID,
			Transport:       head.Transport,
			SampleRate:      DefaultDownloadRate,
			FrameDurationMs: FrameDurationMs,
		}
		if ev.Transport == "" {
			ev.Transport = "websocket"
		}
		if len(head.Audio) > 0 {
			var ap struct {
				SampleRate    int `json:"sample_rate"`
				FrameDuration int `json:"frame_duration"`
			}
			if json.Unmarshal(head.Audio, &ap) == nil {
				if ap.SampleRate > 0 {
					ev.SampleRate = ap.SampleRate
				}
				if ap.FrameDuration > 0 {
					ev.FrameDurationMs = ap.FrameDuration
				}
			}
		}
		return ev, nil
	case "stt":
		return EventStt{Text: head.Text}, nil
	case "tts":
		state := head.State
		if state == "" {
			state = "sentence_start"
		}
		return EventTts{State: state, Text: head.Text}, nil
	case "llm":
		return EventLlm{Text: head.Text, Emotion: head.Emotion}, nil
	case "alert":
		return EventAlert{Status: head.Status, Message: head.Message}, nil
	case "goodbye":
		return EventGoodbye{}, nil
	case "mcp":
		var payload struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if len(head.Payload) > 0 {
			_ = json.Unmarshal(head.Payload, &payload)
		}
		if payload.Method == "" {
			return EventUnknown{Type: head.Type}, nil
		}
		return EventMcp{
			ID:        payload.ID,
			Method:    payload.Method,
			Params:    payload.Params,
			SessionID: head.SessionID,
		}, nil
	default:
		return EventUnknown{Type: head.Type}, nil
	}
}

// mcpEnvelope 拼 MCP 回包的信封：{"session_id":..,"type":"mcp","payload":{"jsonrpc":"2.0","id":..,...}}
func mcpEnvelope(id json.RawMessage, sessionID string) map[string]any {
	payload := map[string]any{"jsonrpc": "2.0"}
	if len(id) > 0 {
		payload["id"] = id
	} else {
		payload["id"] = nil
	}
	return map[string]any{
		"session_id": sessionID,
		"type":       "mcp",
		"payload":    payload,
	}
}

// McpResultJSON 构造 MCP 成功回包，result 会被原样序列化。
func McpResultJSON(id json.RawMessage, result any, sessionID string) []byte {
	root := mcpEnvelope(id, sessionID)
	root["payload"].(map[string]any)["result"] = result
	b, _ := json.Marshal(root)
	return b
}

// McpErrorJSON 构造 MCP 错误回包。
func McpErrorJSON(id json.RawMessage, code int, message, sessionID string) []byte {
	root := mcpEnvelope(id, sessionID)
	root["payload"].(map[string]any)["error"] = map[string]any{
		"code":    code,
		"message": message,
	}
	b, _ := json.Marshal(root)
	return b
}

// OtaActivation 未绑定时服务端给的配对信息。
type OtaActivation struct {
	Code      string
	Message   string
	Challenge string
}

// OtaResult OTA 响应。Websocket 与 Activation 二者只会有一个。
type OtaResult struct {
	WsURL           string
	Token           string
	Version         int
	Activation      *OtaActivation
	FirmwareVersion string
}

// ParseOta 解析 OTA 响应；字段缺失返回对应空值，不报错。
func ParseOta(raw []byte) (*OtaResult, error) {
	var root struct {
		Websocket *struct {
			URL     string `json:"url"`
			Token   string `json:"token"`
			Version int    `json:"version"`
		} `json:"websocket"`
		Activation *struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Challenge string `json:"challenge"`
		} `json:"activation"`
		Firmware *struct {
			Version string `json:"version"`
		} `json:"firmware"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	out := &OtaResult{}
	if root.Websocket != nil && root.Websocket.URL != "" {
		out.WsURL = root.Websocket.URL
		out.Token = root.Websocket.Token
		out.Version = root.Websocket.Version
		if out.Version == 0 {
			out.Version = ProtocolVersion
		}
	}
	if root.Activation != nil && root.Activation.Code != "" {
		out.Activation = &OtaActivation{
			Code:      root.Activation.Code,
			Message:   root.Activation.Message,
			Challenge: root.Activation.Challenge,
		}
	}
	if root.Firmware != nil {
		out.FirmwareVersion = root.Firmware.Version
	}
	return out, nil
}

// OtaRequestBody 构造 OTA 请求体（version 2），字段照官方 esp32 固件打。
// 服务端只从 OTA 请求知道这台设备说什么语言，所以 language 必须带上。
func OtaRequestBody(mac, clientID, appVersion, boardName string) []byte {
	body := map[string]any{
		"version":     2,
		"language":    DeviceLanguage,
		"mac_address": mac,
		"uuid":        clientID,
		"application": map[string]any{"name": "xiaozhi", "version": appVersion},
		"board":       map[string]any{"type": "generic", "name": boardName, "manufacturer": "", "mac": mac},
		"protocol":    map[string]any{"features": mcpFeatures()},
	}
	b, _ := json.Marshal(body)
	return b
}

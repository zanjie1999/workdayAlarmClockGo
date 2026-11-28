package conf

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// 小智（xiaozhi）语音助手的配置，对应 json 里的 xiaozhi 字段。
//
// 默认关闭：这是可选功能，关着的时候它不占内存也不占麦克风。
// 字段与 Android 端的 XiaozhiConfig 一一对应，方便两边行为一致。
type XiaozhiSettings struct {
	// Enabled 总开关，开启后后台自动连接并监听
	Enabled bool `json:"enabled"`
	// OtaUrl 官方 OTA 地址，用它换连接地址和 token，也用来拿配对码
	OtaUrl string `json:"otaUrl"`
	// Token 手动指定时的鉴权 token，正常由 OTA 下发
	Token string `json:"token"`
	// DeviceMac 设备身份，服务端按它认设备；空表示还没生成
	DeviceMac string `json:"deviceMac"`
	// ClientId 客户端身份，同样是首次生成后固定
	ClientId string `json:"clientId"`
	// AutoContinue 一轮说完后自动继续听，直到静默超时
	AutoContinue bool `json:"autoContinue"`
	// SilenceTimeoutMs 静默多久结束会话（毫秒）
	SilenceTimeoutMs int `json:"silenceTimeoutMs"`
	// PauseMedia 会话期间暂停正在放的音乐，避免外放糊住麦克风
	PauseMedia bool `json:"pauseMedia"`
	// WakeEnabled 语音唤醒开关；关掉后只能手动开始会话
	WakeEnabled bool `json:"wakeEnabled"`
}

const (
	XiaozhiOtaDefault = "https://api.tenclass.net/xiaozhi/ota/"
	XiaozhiWsDefault  = "wss://api.tenclass.net/xiaozhi/v1/"
	// XiaozhiWakeWord 唤醒词固定为"小度小度"：用的是音箱自带的模型，不能改
	XiaozhiWakeWord    = "小度小度"
	XiaozhiDevLanguage = "zh-CN"
)

// 会话行为固定值：对外不开放，跟 Android 的默认值保持一致
const (
	// XiaozhiMode 会话模式，官方协议里的 auto
	XiaozhiMode = "auto"
	// XiaozhiMinSilenceMs 静默超时下限
	XiaozhiMinSilenceMs = 1000
	// XiaozhiMaxSilenceMs 静默超时上限
	XiaozhiMaxSilenceMs = 60000
)

// DefaultXiaozhi 默认配置：关闭、走官方服务器、自动模式。
func DefaultXiaozhi() XiaozhiSettings {
	return XiaozhiSettings{
		Enabled:          false,
		OtaUrl:           XiaozhiOtaDefault,
		Token:            "",
		DeviceMac:        "",
		ClientId:         "",
		AutoContinue:     true,
		SilenceTimeoutMs: 8000,
		PauseMedia:       true,
		WakeEnabled:      false,
	}
}

// NormalizeXiaozhi 把配置整理成合法值。
// 老配置文件没有这个字段时全是零值，靠这里回落默认；越界的数值统一夹取，
// 免得手改 json 或拼错参数把会话搞成连不上又看不出原因。
func NormalizeXiaozhi(x XiaozhiSettings) XiaozhiSettings {
	out := x

	out.OtaUrl = NormalizeXiaozhiOtaUrl(x.OtaUrl)
	if out.SilenceTimeoutMs < XiaozhiMinSilenceMs {
		out.SilenceTimeoutMs = 8000
	}
	if out.SilenceTimeoutMs > XiaozhiMaxSilenceMs {
		out.SilenceTimeoutMs = XiaozhiMaxSilenceMs
	}
	out.DeviceMac = NormalizeXiaozhiMac(x.DeviceMac)
	out.Token = strings.TrimSpace(x.Token)
	out.ClientId = strings.TrimSpace(x.ClientId)
	return out
}

// NormalizeXiaozhiOtaUrl 没写协议头时按 https 处理。
func NormalizeXiaozhiOtaUrl(raw string) string {
	url := strings.TrimSpace(raw)
	if url == "" {
		return XiaozhiOtaDefault
	}
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		url = "https://" + url
	}
	return url
}

// NormalizeXiaozhiMac 统一成小写冒号分隔；为空则返回空（由 EnsureXiaozhiIdentity 生成）。
func NormalizeXiaozhiMac(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.ReplaceAll(v, "-", ":")
	return v
}

// NewXiaozhiMac 生成一个随机 MAC 当设备身份：
// 首字节置本地管理位、清组播位，保证是合法的单播本地地址。
// 不读真实网卡——读不到，而且同一台机器刷两次固件会撞车。
func NewXiaozhiMac() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "02:00:00:00:00:01"
	}
	b[0] = (b[0] & 0xFC) | 0x02
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

// NewXiaozhiClientID 生成一个随机 UUID v4 当客户端身份。
func NewXiaozhiClientID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// EnsureXiaozhiIdentity 保证设备身份存在，缺失就生成并写回配置。
// 身份必须稳定，否则每次连接都会被服务端当成一台新设备，配对码也会变。
func EnsureXiaozhiIdentity() bool {
	changed := false
	if NormalizeXiaozhiMac(Cfg.Xiaozhi.DeviceMac) == "" {
		Cfg.Xiaozhi.DeviceMac = NewXiaozhiMac()
		changed = true
	}
	if Cfg.Xiaozhi.ClientId == "" {
		Cfg.Xiaozhi.ClientId = NewXiaozhiClientID()
		changed = true
	}
	if changed {
		Save()
	}
	return changed
}

package xiaozhi

import (
	"errors"

	"workdayAlarmClock/conf"
)

// Status 给设置页面看的运行状态快照。
// 放在不带 build tag 的文件里，stub 和真实实现共用同一份定义。
type Status struct {
	// Running 后台线程是否在跑
	Running bool `json:"running"`
	// State idle / connecting / pairing / handshaking / listening / thinking / speaking / error
	State string `json:"state"`
	// Backend 实际用的采集后端
	Backend string `json:"backend"`
	// ActivationCode 未绑定时显示的配对码，绑定后清空
	ActivationCode string `json:"activationCode"`
	// ActivationMessage 配对提示
	ActivationMessage string `json:"activationMessage"`
	// LastKind 最近一条文本的类型：stt / llm / tts
	LastKind string `json:"lastKind"`
	// LastText 最近一条文本
	LastText string `json:"lastText"`
	// Error 最近一次失败原因
	Error string `json:"error"`
	// UpdatedAt 状态更新时间（Unix 秒）
	UpdatedAt int64 `json:"updatedAt"`
}

// Options 启动参数。
type Options struct {
	// Cfg 配置快照
	Cfg conf.XiaozhiSettings
	// Version 上报给服务端的版本号
	Version string
	// BoardName 上报的板子名
	BoardName string
	// ALSAOutput 播放设备名，空用 default
	ALSAOutput string
	// ALSAInput 采集设备名，空用 default
	ALSAInput string
}

// ActivationResult 一次"检查激活状态"的结果。
type ActivationResult struct {
	// Activated 服务端已经认这台设备
	Activated bool `json:"activated"`
	// Code 未绑定时服务端给的配对码
	Code string `json:"code"`
	// Message 服务端给的提示文案
	Message string `json:"message"`
}

// ErrUnavailable 当前环境没有可用的音频后端。
var ErrUnavailable = errors.New("当前设备没有可用的音频后端")

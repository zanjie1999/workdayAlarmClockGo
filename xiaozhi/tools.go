//go:build linux && cgo && alsa && !app

package xiaozhi

import (
	"encoding/json"
	"errors"

	"workdayAlarmClock/command"
	"workdayAlarmClock/xiaozhi/core"
)

// 工具名与 Android 端保持一致（`module.action` 风格）
const (
	toolPlayer = "self.player.command"
	toolStatus = "self.get_device_status"
)

// toolPlayerDesc 给模型看的命令清单，与 Android 端一字不差。
const toolPlayerDesc = `控制本机的音乐播放器和闹钟，优先使用，只能原样发送下面这些命令：
播放默认歌单：1key
播放歌曲：playmusic {歌曲id}
收到明确的歌曲id时直接执行 playmusic {歌曲id}，不要先查询或搜索歌曲信息，接口不互通
播放歌单：playlist {歌单id}
播放链接：play {音频url}
暂停播放：pause
继续播放：resume
下一首：next
上一首：prev
停止播放：stop
定时停止播放：stop {分钟}
音量加：volp
音量减：volm
设置音量：vol {1-100}
查询闹钟：alarmlist（返回一行json，格式 {"alarms":[{"time":"0730","types":["1","6"]}]}）
添加闹钟：alarmadd {HHmm} {类型,类型}
删除闹钟：alarmdel {HHmm} {类型,类型}（只删掉对得上的类型）
查询正在播放：nowplay（返回一行json，格式 {"nowPlaying":"歌名"}，没在播时为空）
闹钟时间用24小时制4位数字，例如0730
闹钟类型用半角逗号分隔：1法定工作日 2法定休息日 3仅一次 4每天 5周日 6周一 7周二 8周三 9周四 10周五 11周六，4位数字表示某月某日如0101
删除前先用alarmlist查一下，类型要和已有闹钟对得上才删`

// toolStatusDesc 设备状态工具的说明。
const toolStatusDesc = `查询本机的实时状态，用户问电量、有没有在充电、音量多大、IP地址是多少、屏幕亮没亮、还剩多少存储和内存、现在几点几分几号星期几时都用它。
不需要参数，返回一行json，例如 {"battery":87,"charging":true,"volume":40,"screen":"on","brightness":3,"ip":"192.168.1.23","storageFreeMB":1024,"memFreeMB":210,"time":"2026-11-17 03:04 周二","playing":false,"version":"1.4.2"}
电量或音量读不到时对应字段是 -1`

// deviceTools 暴露给 AI 的工具。Linux 上没有摄像头，所以只有播放器和状态两个。
type deviceTools struct{}

func (deviceTools) Tools() []core.Tool {
	return []core.Tool{
		{
			Name:        toolPlayer,
			Description: toolPlayerDesc,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "完整命令原文，例如 1key、playmusic 1234、stop 30、vol 40、alarmlist、alarmadd 0730 1,6",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        toolStatus,
			Description: toolStatusDesc,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

func (deviceTools) Call(name string, args json.RawMessage) (core.ToolResult, error) {
	switch name {
	case toolPlayer:
		var a struct {
			Command string `json:"command"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		// 必须过白名单：模型输出等于外部输入，不能让它拼出 rm/exit 这类命令
		cmd, ok := command.Parse(a.Command)
		if !ok {
			return core.ToolResult{}, errors.New("不支持的播放器命令：" + a.Command)
		}
		// 点歌这类命令执行完这轮就收场，别让麦克风一直开着
		return core.ToolResult{
			Text:    command.Run(cmd),
			EndTurn: command.EndsConversation(cmd),
		}, nil
	case toolStatus:
		return core.ToolResult{Text: command.Status()}, nil
	}
	return core.ToolResult{}, errors.New("未知工具：" + name)
}

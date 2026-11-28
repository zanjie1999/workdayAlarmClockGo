// Package command 是播放器与闹钟的命令入口。
//
// go shellInput() 从 stdin 读的、Android 通过管道写进来的、小智语音通过 MCP 下发的，
// 走的都是这里的同一套实现——三边行为必须一致，所以逻辑只能有一份。
package command

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"workdayAlarmClock/alarm"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/player"
)

// 语音下发的命令白名单，和 Android 端 XiaozhiPlayerCommands 完全一致。
//
// 必须白名单：模型的输出等于外部输入，放任它随便拼字符串就等于把整台设备的
// 控制权交出去（`rm`、`touch`、`exit` 这些 shell 命令只留在本地 shell 里）。
const (
	maxStopMinutes      = 1440
	maxIDLength         = 64
	maxURLLength        = 2048
	maxAlarmTypesLength = 64
)

// Parse 校验并归一化一条语音命令。第二个返回值 false 表示不认识，不下发。
func Parse(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", false
	}
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(parts) == 0 {
		return "", false
	}
	keyword := strings.ToLower(parts[0])

	switch keyword {
	case "1key", "pause", "resume", "volp", "volm", "next", "prev", "alarmlist", "nowplay":
		if len(parts) != 1 {
			return "", false
		}
		return keyword, true

	case "stop":
		switch len(parts) {
		case 1:
			return "stop", true
		case 2:
			minutes, err := strconv.Atoi(parts[1])
			if err != nil || minutes < 1 || minutes > maxStopMinutes {
				return "", false
			}
			return "stop " + strconv.Itoa(minutes), true
		}
		return "", false

	case "vol":
		if len(parts) != 2 {
			return "", false
		}
		value, err := strconv.Atoi(parts[1])
		if err != nil || value < 1 || value > 100 {
			return "", false
		}
		return "vol " + strconv.Itoa(value), true

	case "playmusic", "playlist":
		if len(parts) != 2 || !isSafeID(parts[1]) {
			return "", false
		}
		return keyword + " " + parts[1], true

	case "play":
		if len(parts) != 2 || !isHTTPURL(parts[1]) {
			return "", false
		}
		return "play " + parts[1], true

	case "alarmadd", "alarmdel":
		if len(parts) != 3 {
			return "", false
		}
		hhmm, ok := normalizeAlarmTime(parts[1])
		if !ok {
			return "", false
		}
		types, ok := normalizeAlarmTypes(parts[2])
		if !ok {
			return "", false
		}
		return keyword + " " + hhmm + " " + types, true
	}
	return "", false
}

// EndsConversation 这条命令执行完，这轮对话就该收场、不再续听。
// 只有"开始/切换播放"这一类算；暂停、音量、闹钟都是可能连着操作的。
func EndsConversation(cmd string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(cmd, " ", 2)[0])) {
	case "1key", "playmusic", "playlist", "play", "resume", "next", "prev":
		return true
	}
	return false
}

// normalizeAlarmTime 归一化成 4 位 HHmm，顺手吃掉 07:30 这种写法。
func normalizeAlarmTime(s string) (string, bool) {
	digits := strings.NewReplacer(":", "", "：", "").Replace(s)
	if len(digits) != 4 {
		return "", false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return digits, true
}

// normalizeAlarmTypes 只查字符，具体取值（1-11 / 月日）交给 alarm 包判断。
func normalizeAlarmTypes(s string) (string, bool) {
	if s == "" || len(s) > maxAlarmTypesLength {
		return "", false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != ',' {
			return "", false
		}
	}
	return s, true
}

// isSafeID 歌曲/歌单 id 只允许字母数字和 _ -，挡住能拼出第二条命令的东西。
func isSafeID(id string) bool {
	if id == "" || len(id) > maxIDLength {
		return false
	}
	for _, c := range id {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func isHTTPURL(url string) bool {
	if len(url) > maxURLLength {
		return false
	}
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	for _, c := range url {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			return false
		}
	}
	return true
}

// Run 执行一条命令，返回一句给调用方看的结果；返回空串表示不认识这条命令。
//
// 输出的副作用（app.Send / 打印）和以前 shell 里的行为一模一样，
// 只是额外把结果文本返回出去，好让语音那边能回一句话。
func Run(cmd string) string {
	switch cmd {
	case "stop":
		player.Stop()
		return "已停止播放"
	case "pause":
		player.Pause()
		return "已暂停"
	case "resume":
		player.Resume()
		return "已继续播放"
	case "volp":
		player.VolUp()
		return "音量已加大"
	case "volm":
		player.VolDown()
		return "音量已减小"
	case "next":
		app.Send(player.NextN(1))
		return "已切到下一首"
	case "prev":
		app.Send(player.Prev())
		return "已切回上一首"
	case "1key":
		app.Send(player.Me1Key())
		return "开始播放默认歌单"
	case "play":
		// 没有参数时：没在播就一键播放，暂停了就继续，否则暂停
		if player.IsStop {
			player.Me1Key()
			return "开始播放"
		}
		if player.IsPaused {
			player.Resume()
			return "已继续播放"
		}
		player.Pause()
		return "已暂停"
	case "alarmlist":
		out := alarm.ListJSON()
		app.SendLocal(out)
		return out
	case "nowplay":
		if player.IsStop {
			app.SendLocal("当前没有播放")
			return "当前没有播放"
		}
		out := "正在播放: " + player.NowPlaying
		app.SendLocal(out)
		return out
	}

	switch {
	case strings.HasPrefix(cmd, "play "):
		player.PlayUrl(cmd[5:])
		return "开始播放链接"
	case strings.HasPrefix(cmd, "vol "):
		player.SetVol(cmd[4:])
		return "音量已设为 " + cmd[4:]
	case strings.HasPrefix(cmd, "playlist "):
		out := player.PlayNePlaylist(cmd[9:], false)
		fmt.Println("播放歌单", out)
		return "开始播放歌单"
	case strings.HasPrefix(cmd, "playmusic "):
		player.PlayNeMusic(cmd[10:], false)
		return "开始播放歌曲"
	case strings.HasPrefix(cmd, "stop "):
		minF, err := strconv.ParseFloat(cmd[5:], 64)
		if err != nil {
			return "定时停止需要分钟数"
		}
		if minF > 0 {
			player.StopUnix = time.Now().Unix() + int64(minF*60)
			out := "将在 " + cmd[5:] + " 分钟后停止播放"
			fmt.Println(out)
			return out
		}
		player.StopUnix = 0
		return "已取消定时停止"
	case strings.HasPrefix(cmd, "alarmadd "):
		hhmm, typeS := alarm.Args(cmd[9:])
		if !alarm.Add(hhmm, typeS) {
			return "闹钟参数不合法"
		}
		return "已添加闹钟 " + hhmm
	case strings.HasPrefix(cmd, "alarmdel "):
		hhmm, typeS := alarm.Args(cmd[9:])
		if !alarm.DelTypes(hhmm, typeS) {
			return "闹钟不存在或类型对不上"
		}
		return "已删除闹钟 " + hhmm
	}
	return ""
}

// Status 设备状态，一行紧凑 JSON，直接当 MCP 工具结果返回。
// 键名和 Android 端 deviceStatusJson 一致；读不到的字段给 -1。
func Status() string {
	battery := -1
	charging := false
	if raw, err := os.ReadFile("/sys/class/power_supply/battery/capacity"); err == nil {
		if v, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
			battery = v
		}
	}
	if raw, err := os.ReadFile("/sys/class/power_supply/battery/status"); err == nil {
		s := strings.ToLower(strings.TrimSpace(string(raw)))
		charging = s == "charging" || s == "full"
	}

	screen := "off"
	brightness := -1
	if raw, err := os.ReadFile("/sys/class/backlight/pwm-backlight.0/brightness"); err == nil {
		if v, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
			brightness = v
			if v > 0 {
				screen = "on"
			}
		}
	}

	ip, _ := app.GetLocalIP()

	playing := !player.IsStop && !player.IsPaused

	status := map[string]any{
		"battery":  battery,
		"charging": charging,
		// 音量在 ALSA 那边没有统一读法，读不到就给 -1（和 Android 的约定一致）
		"volume":        -1,
		"screen":        screen,
		"brightness":    brightness,
		"ip":            ip,
		"storageFreeMB": freeStorageMB(),
		"memFreeMB":     freeMemMB(),
		"time":          time.Now().Format("2006-01-02 15:04") + " " + weekdayCN(time.Now()),
		"playing":       playing,
		"version":       conf.Version,
	}
	return jsonLine(status)
}

// jsonLine 拼一行紧凑 JSON，键按字母序，方便肉眼对比。
func jsonLine(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(k))
		b.WriteByte(':')
		switch v := m[k].(type) {
		case string:
			b.WriteString(strconv.Quote(v))
		case int:
			b.WriteString(strconv.Itoa(v))
		case bool:
			if v {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		default:
			b.WriteString("null")
		}
	}
	b.WriteByte('}')
	return b.String()
}

func weekdayCN(t time.Time) string {
	return [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[int(t.Weekday())]
}

// freeMemMB 读 /proc/meminfo 的 MemAvailable（老内核没有就退回 MemFree）。
func freeMemMB() int {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}
	var free, avail int
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemAvailable:":
			avail, _ = strconv.Atoi(fields[1])
		case "MemFree:":
			free, _ = strconv.Atoi(fields[1])
		}
	}
	kb := avail
	if kb == 0 {
		kb = free
	}
	if kb == 0 {
		return -1
	}
	return kb / 1024
}

// freeStorageMB 取应用目录所在分区的剩余空间。
func freeStorageMB() int {
	return freeSpaceMB(conf.Cfg.SavePath)
}

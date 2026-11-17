/*
 * 闹钟编辑：添加、删除、查询
 * shell 和网页端共用同一套校验、保存和重排唤醒逻辑
 */

package alarm

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
)

// Add 添加闹钟。typeS 支持逗号分隔，不传按一次性闹钟。
func Add(hhmm, typeS string) bool {
	hhmm = normalizeHhmm(hhmm)
	if hhmm == "" {
		app.SendLocal("闹钟时间不合法，应为 HHmm 例如 0730")
		return false
	}
	if strings.TrimSpace(typeS) == "" {
		typeS = "3"
	}
	types, ok := normalizeAlarmTypes(typeS)
	if !ok {
		app.SendLocal("闹钟类型不合法")
		return false
	}
	if conf.Cfg.Alarm == nil {
		conf.Cfg.Alarm = map[string][]string{}
	}
	conf.Cfg.Alarm[hhmm] = append(conf.Cfg.Alarm[hhmm], types...)
	conf.Save()
	SendNextWake()
	app.SendLocal("已添加闹钟 " + hhmm + " " + strings.Join(conf.Cfg.Alarm[hhmm], ","))
	return true
}

// DelTypes 只删对得上的类型，一条都不匹配就原样留着。
func DelTypes(hhmm, typeS string) bool {
	hhmm = normalizeHhmm(hhmm)
	if hhmm == "" {
		app.SendLocal("闹钟时间不合法，应为 HHmm 例如 0730")
		return false
	}
	types, ok := normalizeAlarmTypes(typeS)
	if !ok {
		app.SendLocal("闹钟类型不合法")
		return false
	}
	dayTypeList, exists := conf.Cfg.Alarm[hhmm]
	if !exists {
		app.SendLocal("没有 " + hhmm + " 的闹钟")
		return false
	}
	var kept []string
	for _, dayType := range dayTypeList {
		matched := false
		for _, t := range types {
			if t == dayType {
				matched = true
				break
			}
		}
		if !matched {
			kept = append(kept, dayType)
		}
	}
	if len(kept) == len(dayTypeList) {
		app.SendLocal(hhmm + " 没有对得上的闹钟类型，未删除")
		return false
	}
	if len(kept) == 0 {
		delete(conf.Cfg.Alarm, hhmm)
	} else {
		conf.Cfg.Alarm[hhmm] = kept
	}
	conf.Save()
	SendNextWake()
	if left, ok := conf.Cfg.Alarm[hhmm]; ok {
		app.SendLocal("已删除，剩 " + hhmm + " " + strings.Join(left, ","))
	} else {
		app.SendLocal("已删除闹钟 " + hhmm)
	}
	return true
}

// Del 删掉整个时间点，网页端的一键删除用。
func Del(hhmm string) bool {
	hhmm = normalizeHhmm(hhmm)
	if hhmm == "" {
		app.SendLocal("闹钟时间不合法，应为 HHmm 例如 0730")
		return false
	}
	if _, exists := conf.Cfg.Alarm[hhmm]; !exists {
		app.SendLocal("没有 " + hhmm + " 的闹钟")
		return false
	}
	delete(conf.Cfg.Alarm, hhmm)
	conf.Save()
	SendNextWake()
	app.SendLocal("已删除闹钟 " + hhmm)
	return true
}

// List 打印一行 JSON：{"alarms":[{"time":"0730","types":["1","6"]}]}
func List() {
	keys := make([]string, 0, len(conf.Cfg.Alarm))
	for hhmm := range conf.Cfg.Alarm {
		keys = append(keys, hhmm)
	}
	sort.Strings(keys)

	type entry struct {
		Time  string   `json:"time"`
		Types []string `json:"types"`
	}
	alarms := make([]entry, 0, len(keys))
	for _, hhmm := range keys {
		types := conf.Cfg.Alarm[hhmm]
		if types == nil {
			types = []string{}
		}
		alarms = append(alarms, entry{Time: hhmm, Types: types})
	}
	b, err := json.Marshal(map[string]interface{}{"alarms": alarms})
	if err != nil {
		app.SendLocal("闹钟序列化失败")
		return
	}
	app.SendLocal(string(b))
}

// Args 把 shell 的整串参数拆成 时间 + 类型。
func Args(arg string) (string, string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", ""
	}
	// 不传类型按一次性闹钟，和网页端 /addAlarm 的行为一样
	if len(fields) < 2 {
		return fields[0], "3"
	}
	return fields[0], fields[1]
}

// 闹钟时间归一化成 4 位，00:00 和 0:00 这类手滑写法也认
func normalizeHhmm(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "：", ""), ":", "")
	if len(s) == 3 {
		s = "0" + s
	}
	if len(s) != 4 {
		return ""
	}
	for i := 0; i < 4; i++ {
		if s[i] < '0' || s[i] > '9' {
			return ""
		}
	}
	hh, _ := strconv.Atoi(s[:2])
	mm, _ := strconv.Atoi(s[2:])
	if hh > 23 || mm > 59 {
		return ""
	}
	return s
}

// 类型：1 法定工作日 2 法定休息日 3 一次 4 每天 5~11 周日至周六，4 位是月日
func normalizeAlarmTypes(s string) ([]string, bool) {
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(t) == 4 {
			mm, _ := strconv.Atoi(t[:2])
			dd, _ := strconv.Atoi(t[2:])
			if mm < 1 || mm > 12 || dd < 1 || dd > 31 {
				return nil, false
			}
			out = append(out, t)
			continue
		}
		n, err := strconv.Atoi(t)
		if err != nil || n < 1 || n > 11 {
			return nil, false
		}
		out = append(out, strconv.Itoa(n))
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

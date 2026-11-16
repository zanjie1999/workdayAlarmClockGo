/*
 * 闹钟命令行入口，主要给 App 里的小智 AI 用
 * alarmlist                     查询，打一行 json 给上层
 * alarmadd 0730 1,6             添加，类型不传按一次性闹钟（和网页端一致）
 * alarmdel 0730 1,6             只删掉对得上的类型，删空了整条去掉
 */

package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
)

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

// 查闹钟，输出一行 json：{"alarms":[{"time":"0730","types":["1","6"]}]}
func alarmList() {
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

func alarmAdd(hhmm, typeS string) {
	hhmm = normalizeHhmm(hhmm)
	if hhmm == "" {
		app.SendLocal("闹钟时间不合法，应为 HHmm 例如 0730")
		return
	}
	types, ok := normalizeAlarmTypes(typeS)
	if !ok {
		app.SendLocal("闹钟类型不合法")
		return
	}
	if conf.Cfg.Alarm == nil {
		conf.Cfg.Alarm = map[string][]string{}
	}
	conf.Cfg.Alarm[hhmm] = append(conf.Cfg.Alarm[hhmm], types...)
	conf.Save()
	notifyAppAlarm()
	app.SendLocal("已添加闹钟 " + hhmm + " " + strings.Join(conf.Cfg.Alarm[hhmm], ","))
}

// 只删对得上的类型，一条都不匹配就原样留着
func alarmDel(hhmm, typeS string) {
	hhmm = normalizeHhmm(hhmm)
	if hhmm == "" {
		app.SendLocal("闹钟时间不合法，应为 HHmm 例如 0730")
		return
	}
	types, ok := normalizeAlarmTypes(typeS)
	if !ok {
		app.SendLocal("闹钟类型不合法")
		return
	}
	dayTypeList, exists := conf.Cfg.Alarm[hhmm]
	if !exists {
		app.SendLocal("没有 " + hhmm + " 的闹钟")
		return
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
		return
	}
	if len(kept) == 0 {
		delete(conf.Cfg.Alarm, hhmm)
	} else {
		conf.Cfg.Alarm[hhmm] = kept
	}
	conf.Save()
	notifyAppAlarm()
	if left, ok := conf.Cfg.Alarm[hhmm]; ok {
		app.SendLocal("已删除，剩 " + hhmm + " " + strings.Join(left, ","))
	} else {
		app.SendLocal("已删除闹钟 " + hhmm)
	}
}

// 有闹钟才需要 App 的每分钟唤醒
func notifyAppAlarm() {
	if !conf.IsApp {
		return
	}
	if len(conf.Cfg.Alarm) > 0 {
		app.Send("ALARMON")
	} else {
		app.Send("ALARMOFF")
	}
}

// shellInput 里把整串参数拆成 时间 + 类型
func alarmArgs(arg string) (string, string) {
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

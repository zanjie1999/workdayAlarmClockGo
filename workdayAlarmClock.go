/*
 * 工作日闹钟 Go
 * zyyme 20230630
 * v1.0
 */

package main

import (
	"log"
	"os"
	"time"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/player"
	"workdayAlarmClock/router"

	"github.com/zanjie1999/httpme"
)

// 获取今天是不是工作日
func workDayApi() {
	req := httpme.Httpme()
	resp, err := req.Get("https://timor.tech/api/holiday/info/" + time.Now().Format("2006-01-02"))
	if err == nil {
		var j map[string]interface{}
		resp.Json(&j)
		if j["code"].(float64) != 200 {
			conf.IsWorkDay = j["type"].(map[string]interface{})["type"].(float64) == 0
			log.Println(j["type"].(map[string]interface{})["name"], "工作日吗？", conf.IsWorkDay)
			return
		}
	}
	log.Println("获取工作日信息出错", err)
	conf.IsWorkDay = time.Now().Weekday() != time.Saturday && time.Now().Weekday() != time.Sunday
}

// 定时器 go timer()
func timer() {
	for {
		hhmm := time.Now().Format("1504")
		if dayType, ok := conf.Cfg.Alarm[hhmm]; ok {
			if (dayType == 1 && conf.IsWorkDay) || (dayType == 2 && !conf.IsWorkDay) || dayType == 4 {
				log.Println("闹钟时间到", hhmm)
				player.PlayAlarm()
			} else if dayType == 3 {
				log.Println("闹钟时间到", hhmm)
				player.PlayAlarm()
				delete(conf.Cfg.Alarm, hhmm)
				conf.Save()
			}
		}
		// 秒对齐
		time.Sleep(time.Duration(60-time.Now().Unix()%60) * time.Second)
	}
}

func main() {
	// libWorkdayAlarmClock.so app
	if len(os.Args) > 1 && os.Args[1] == "app" {
		conf.IsApp = true
		httpme.SetDns("223.6.6.6:53")
		// 时区不对，设置成中国+8
		time.Local = time.FixedZone("CST", 8*3600)
	}
	log.Println("当前时区", time.Local)
	conf.Init()
	workDayApi()
	go timer()
	router.Init("/").Run(":8080")
}

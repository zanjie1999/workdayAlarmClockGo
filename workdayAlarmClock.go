/*
 * 工作咩闹钟 Go
 * zyyme 20230630
 */

package main

import (
	"fmt"
	"log"
	"os"
	"time"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/player"
	"workdayAlarmClock/router"
	"workdayAlarmClock/weather"

	"github.com/zanjie1999/httpme"
)

var VERSION = "4.2"

// 获取今天是不是工作日
func workDayApi() {
	req := httpme.Httpme()
	resp, err := req.Get("https://timor.tech/api/holiday/info/" + time.Now().Format("2006-01-02"))
	if err == nil {
		var j map[string]interface{}
		resp.Json(&j)
		if j["code"].(float64) != 200 {
			conf.IsWorkDay = j["type"].(map[string]interface{})["type"].(float64) == 0 || j["type"].(map[string]interface{})["type"].(float64) == 3
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
		if hhmm == "0000" {
			workDayApi()
		}
		if hhmm == "2300" {
			weather.GetWeather("")
		}
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

// 处理shell输入 go shellInput()
func shellInput() {
	for {
		var cmd string
		_, err := fmt.Scanln(&cmd)
		if err != nil {
			fmt.Println("输入错误", err)
		} else {
			switch cmd {
			case "stop":
				player.Stop()
			case "next":
				player.Next()
			case "exit":
				os.Exit(0)
			default:
				fmt.Println("未知命令", cmd)
			}
		}
	}
}

func main() {
	// libWorkdayAlarmClock.so app
	if len(os.Args) > 1 {
		if os.Args[1] == "app" {
			conf.IsApp = true
			httpme.SetDns("223.6.6.6:53")
		} else {
			player.ShellPlayer = os.Args[1]
		}
	}
	if !conf.IsApp {
		log.Println("使用音乐播放器：", player.ShellPlayer)
	}
	conf.Init()
	// 设置时区
	time.Local = time.FixedZone("UTC+", conf.Cfg.Tz*3600)
	log.Println("工作咩闹钟 v" + VERSION)
	log.Println("当前时区", time.Local, conf.Cfg.Tz)
	workDayApi()
	go timer()
	go shellInput()
	router.Init("/").Run(":8080")
}

/*
 * 工作日闹钟 Go
 * zyyme 20230630
 * v1.0
 */

package main

import (
	"log"
	"time"
	"workdayAlarmClock/player"

	"github.com/asmcos/requests"
)

var (
	isWorkDay = false
	// 闹钟时间 24小时制hhmm 工作日0 休息日1
	AlarmCfg = map[string]int{
		"0710": 0,
		"2328": 1,
	}
)

// 获取今天是不是工作日
func workDayApi() {
	req := requests.Requests()
	resp, err := req.Get("https://timor.tech/api/holiday/info/" + time.Now().Format("2006-01-02"))
	if err == nil {
		var j map[string]interface{}
		resp.Json(&j)
		if j["code"].(float64) != 200 {
			isWorkDay = j["type"].(map[string]interface{})["type"].(float64) == 0
			log.Println(j["type"].(map[string]interface{})["name"], "工作日吗？", isWorkDay)
			return
		}
	}
	log.Println("获取工作日信息出错", err)
	isWorkDay = time.Now().Weekday() != time.Saturday && time.Now().Weekday() != time.Sunday
}

// 定时器 go timer()
func timer() {
	for {
		hhmm := time.Now().Format("1504")
		if dayType, ok := AlarmCfg[hhmm]; ok {
			if (dayType == 0 && isWorkDay) || (dayType == 1 && !isWorkDay) {
				log.Println("闹钟时间到", hhmm)
				player.PlayAlarm()
			}
		}
		time.Sleep(time.Duration(60-time.Now().Unix()%60) * time.Second)
	}
}

func main() {
	player.PlayUrl("https://music.163.com/song/media/outer/url?id=1861402641")
	// log.Println(nemusic.MusicUrl("1861402641"))
	// log.Println(nemusic.PlayList("2236121100"))
	// workDayApi()
	// go timer()
	// router.Init("/").Run(":8080")
}

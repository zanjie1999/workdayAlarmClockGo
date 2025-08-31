/*
 * 工作咩闹钟 Go
 * zyyme 20230630
 */

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/player"
	"workdayAlarmClock/router"
	"workdayAlarmClock/weather"

	"github.com/zanjie1999/httpme"
)

var (
	VERSION       = "18.0"
	workDayApiErr = false
	lasthhmm      = ""
)

// 获取今天是不是工作日
func workDayApi() {
	log.Println("正在获取工作日状态，如时间很长请检查网络")
	req := httpme.Httpme()
	yymmdd := time.Now().Format("2006-01-02")
	if yymmdd == "1970-01-01" {
		log.Println("等待时间同步再获取工作日信息")
		workDayApiErr = true
		return
	}
	resp, err := req.Get("https://timor.tech/api/holiday/info/" + yymmdd)
	if err == nil {
		var j map[string]interface{}
		resp.Json(&j)
		if j["code"].(float64) != 200 {
			conf.IsWorkDay = j["type"].(map[string]interface{})["type"].(float64) == 0 || j["type"].(map[string]interface{})["type"].(float64) == 3
			log.Println(j["type"].(map[string]interface{})["name"], "工作日吗？", conf.IsWorkDay)
			workDayApiErr = false
			return
		}
	}
	workDayApiErr = true
	log.Println("获取工作日信息出错", err)
	conf.IsWorkDay = time.Now().Weekday() != time.Saturday && time.Now().Weekday() != time.Sunday
}

// 定时器 go timer()
func timer() {
	for {
		timeJob()
		// 秒对齐
		time.Sleep(time.Duration(60-time.Now().Unix()%60) * time.Second)
	}
}

func timeJob() {
	now := time.Now()
	mmdd := now.Format("0102")
	hhmm := now.Format("1504")
	if lasthhmm == hhmm {
		log.Print("定时器重复执行", hhmm)
		return
	}
	lasthhmm = hhmm

	// 如出错则每分钟重试 比如刚开机时间是1970-01-01或是压根没网
	if workDayApiErr || hhmm == "0000" {
		workDayApi()
	}
	if hhmm == conf.Cfg.WeatherUpdate {
		weather.GetWeather("")
	}
	if dayTypeList, ok := conf.Cfg.Alarm[hhmm]; ok {
		// 增加 同时间 多类型 的闹钟支持
		for _, dayType := range dayTypeList {
			//  法定工作日                           法定休息日                           每天            周 日一二三四五六
			if (dayType == "1" && conf.IsWorkDay) || (dayType == "2" && !conf.IsWorkDay) || dayType == "4" || dayType == strconv.Itoa(int(now.Weekday())+5) {
				log.Println("闹钟时间到", hhmm)
				player.PlayAlarm()
				break
			} else if dayType == "3" {
				// 一次性闹钟
				log.Println("一次性闹钟时间到", hhmm)
				player.PlayAlarm()
				if len(dayTypeList) == 1 {
					delete(conf.Cfg.Alarm, hhmm)
				} else {
					// 只删掉这条3的
					for i, v := range dayTypeList {
						if v == "3" {
							conf.Cfg.Alarm[hhmm] = append(dayTypeList[:i], dayTypeList[i+1:]...)
						}
					}
				}
				conf.Save()
				if conf.IsApp {
					if len(conf.Cfg.Alarm) > 0 {
						fmt.Println("ALARMON")
					} else {
						fmt.Println("ALARMOFF")
					}
				}
				break
			} else if dayType == mmdd {
				// 月日
				log.Println("闹钟时间到", mmdd, "的", hhmm)
				player.PlayAlarm()
				break
			}
		}
	}
}

// 处理shell输入 go shellInput()
func shellInput() {
	reader := bufio.NewReader(os.Stdin)
	for {
		cmd, err := reader.ReadString('\n')
		cmd = strings.TrimSpace(cmd)
		if err != nil {
			fmt.Println("输入错误", err)
			break
		} else {
			switch cmd {
			case "stop":
				player.Stop()
			case "next":
				fmt.Println(player.Next())
			case "prev":
				fmt.Println(player.Prev())
			case "1key":
				fmt.Println(player.Me1Key())
			case "exit":
				if conf.IsApp {
					fmt.Println("程序已退出，可以使用shell命令或使用 echo EXIT 退出App")
				}
				os.Exit(0)
			case "wake":
				timeJob()
			default:
				if strings.HasPrefix(cmd, "playlist ") {
					player.PlayPlaylist(cmd[9:], false)
				} else {
					fmt.Println("未知命令", cmd)
				}
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
	// 全局禁用TLS验证 兼容老系统
	httpme.SetSkipVerify(true)

	if !conf.IsApp {
		log.Println("使用音乐播放器：", player.ShellPlayer)
	}
	conf.Init()
	if conf.IsApp && conf.Cfg.Wakelock {
		fmt.Println("WAKELOCK")
	}
	if conf.IsApp && len(conf.Cfg.Alarm) > 0 {
		fmt.Println("ALARMON")
	}
	// 设置时区
	time.Local = time.FixedZone("UTC+", conf.Cfg.Tz*3600)
	log.Println("工作咩闹钟 v" + VERSION)
	log.Println("当前时区", time.Local, conf.Cfg.Tz)
	workDayApi()
	if conf.IsApp && conf.Cfg.Wakelock {
		// Android在有闹钟时有每分钟的定时器，在启动Wakelock时将使用双重定时器保证一定会被调用
		go timer()
	}
	go shellInput()
	router.Init("/").Run(":8080")
}

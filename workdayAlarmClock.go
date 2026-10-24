/*
 * 工作咩闹钟 Go
 * zyyme 20230630
 */

package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/nemusic"
	"workdayAlarmClock/player"
	"workdayAlarmClock/router"
	"workdayAlarmClock/update"
	"workdayAlarmClock/weather"

	"github.com/zanjie1999/httpme"
)

var (
	VERSION  = "43.1"
	lasthhmm = ""
)

// 定时器 go timer()
func timer() {
	for {
		timeJob()
		// 秒对齐
		time.Sleep(time.Second)
		time.Sleep(time.Duration(60-time.Now().Unix()%60) * time.Second)
	}
}

func timeJob() {
	now := time.Now()
	// 避免系统时间未同步时运行8点的闹钟
	if now.Year() < 2025 {
		log.Println("请检查系统时间是否正确，当前时间", now.Format("2006-01-02 15:04:05"))
		return
	}
	mmdd := now.Format("0102")
	hhmm := now.Format("1504")
	if lasthhmm == hhmm {
		// log.Print("定时器重复执行", hhmm)
		return
	}
	lasthhmm = hhmm

	// 如出错则每分钟重试 比如刚开机时间是1970-01-01或是压根没网
	if conf.WorkDayApiErr || hhmm == "0000" {
		conf.WorkDayApi()
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
						app.Send("ALARMON")
					} else {
						app.Send("ALARMOFF")
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
			case "pause0":
				player.IsPaused = false
			case "pause1":
				player.IsPaused = true
			case "stop":
				player.Stop()
			case "pause":
				player.Pause()
			case "resume":
				player.Resume()
			case "volp":
				player.VolUp()
			case "volm":
				player.VolDown()
			case "next":
				app.Send(player.Next())
			case "prev":
				app.Send(player.Prev())
			case "1key":
				app.Send(player.Me1Key())
			case "exit":
				if conf.IsApp {
					fmt.Println("程序已退出，可以使用shell命令或使用 echo EXIT 退出App")
				}
				os.Exit(0)
			case "wake":
				timeJob()
			case "testalarm":
				player.PlayAlarm()
			case "savepath":
				fmt.Println("savepath", conf.Cfg.SavePath)
			case "ip":
				ip, _ := app.GetLocalIP()
				fmt.Println(ip)
			case "play":
				// 没有参数时一键播放
				if player.IsStop {
					player.Me1Key()
				} else if player.IsPaused {
					player.Resume()
				} else {
					player.Pause()
				}
			case "weather":
				weather.GetWeather("")
			case "checkupdate":
				p, err := update.Check()
				if err != nil {
					fmt.Println("检查更新失败", err)
				} else if p.HasUpdate {
					fmt.Println("发现新版本 v" + p.Latest + "，更新内容：" + p.Release)
					if p.Asset != "" {
						fmt.Println("输入 update 更新到该版本")
					}
				} else {
					fmt.Println("当前已是最新版本 v" + VERSION)
				}
			case "update":
				// 会替换并重启自己，放goroutine里先把日志吐出去
				go selfUpdate()
			default:
				if strings.HasPrefix(cmd, "play ") {
					player.PlayUrl(cmd[5:])
				} else if strings.HasPrefix(cmd, "vol ") {
					player.SetVol(cmd[4:])
				} else if strings.HasPrefix(cmd, "echo ") {
					app.Send(cmd[5:])
				} else if strings.HasPrefix(cmd, "playlist ") {
					log.Println("播放歌单", player.PlayNePlaylist(cmd[9:], false))
				} else if strings.HasPrefix(cmd, "playmusic ") {
					log.Println("播放歌曲")
					player.PlayNeMusic(cmd[10:], false)
				} else if strings.HasPrefix(cmd, "playlistdl ") {
					nemusic.PlaylistDownload(cmd[11:])
				} else if strings.HasPrefix(cmd, "touch ") {
					f, e := os.Create(cmd[6:])
					f.Close()
					log.Println(e)
				} else if strings.HasPrefix(cmd, "rm ") {
					log.Println(os.Remove(cmd[3:]))
				} else if strings.HasPrefix(cmd, "savepath ") {
					if cmd[9:] == "null" {
						conf.Cfg.SavePath = ""
					} else {
						conf.Cfg.SavePath = cmd[9:]
					}
					conf.Save()
					fmt.Println("SavePath已修改为", conf.Cfg.SavePath)
				} else if strings.HasPrefix(cmd, "stop ") {
					minF, _ := strconv.ParseFloat(cmd[5:], 64)
					if minF > 0 {
						player.StopUnix = time.Now().Unix() + int64(minF*60)
						fmt.Println("将在", minF, "分钟后停止播放")
					} else {
						player.StopUnix = 0
						fmt.Println("已取消定时停止")
					}
				} else {
					fmt.Println("未知命令", cmd)
				}
			}
			// 丢掉在处理过程中输入的命令(比如疯狂的next会堆积起来)
			if reader.Buffered() > 0 {
				reader.Discard(reader.Buffered())
			}
		}
	}
}

func updateRepo() string {
	if conf.IsApp {
		return "zanjie1999/workdayAlarmClockAndroid"
	}
	return "zanjie1999/workdayAlarmClockGo"
}

// 手动更新，会重启进程所以放在goroutine里
func selfUpdate() {
	if conf.IsApp {
		fmt.Println("Android 版请在 App 内更新")
		return
	}
	p, err := update.Check()
	if err != nil {
		fmt.Println("检查更新失败", err)
		return
	}
	if !p.HasUpdate {
		fmt.Println("当前已是最新版本 v" + VERSION)
		return
	}
	if p.Asset == "" {
		fmt.Println("Release 里没有适合当前设备的文件，请手动下载", update.RepoURL()+"/releases")
		return
	}
	if player.IsAlarm || !player.IsStop {
		fmt.Println("正在播放，停止后再更新")
		return
	}
	fmt.Println("正在更新到 v" + p.Latest + " ...")
	if err := update.Apply(p); err != nil {
		fmt.Println("更新失败", err)
		return
	}
	fmt.Println("更新完成，正在重启")
	if err := update.Restart(); err != nil {
		fmt.Println("重启失败，请手动重启程序", err)
	}
}

func checkUpdate() {
	// 检查更新
	p, err := update.Check()
	if err != nil {
		log.Println("检查更新失败", err)
		return
	}
	// 只有远端版本比当前新才提示，当前版本更高（自己编译的）不提示
	if !p.HasUpdate {
		log.Println("当前已是最新版本 v"+VERSION+" 开源仓库：", update.RepoURL())
		return
	}
	log.Println("\n发现新版本v" + p.Latest + "，更新内容：" + p.Release)
	log.Println("下载地址：", update.RepoURL()+"/releases")

	if conf.IsApp {
		app.Send("HASUPDATE")
		app.Send("ECHO 有新版本v" + p.Latest)
		return
	}
	if p.Asset == "" {
		log.Println("Release 里没有适合当前设备的文件，请手动下载")
		return
	}
	// 等一下再动手，避开启动阶段的网络/音频初始化
	time.Sleep(30 * time.Second)
	if player.IsAlarm || !player.IsStop {
		log.Println("正在播放，跳过本次自动更新，可稍后手动更新")
		return
	}
	if err := update.Apply(p); err != nil {
		log.Println("自动更新失败", err)
		return
	}
	log.Println("自动更新完成，正在重启")
	if err := update.Restart(); err != nil {
		log.Println("重启失败，请手动重启程序以使用新版本", err)
	}
}

// 强制把当前目录加入PATH
func addPwd2Path() {
	workingDir, err := os.Getwd()
	if err != nil {
		log.Println("获取当前目录失败，无法更新PATH", err)
		return
	}
	pathValue := os.Getenv("PATH")
	if pathValue == "" {
		pathValue = workingDir
	} else {
		pathValue = workingDir + string(os.PathListSeparator) + pathValue
	}
	if err := os.Setenv("PATH", pathValue); err != nil {
		log.Println("更新PATH失败", err)
	}
}

func main() {
	addPwd2Path()

	// libWorkdayAlarmClock.so app
	if len(os.Args) > 1 {
		if os.Args[1] == "app" {
			conf.IsApp = true
			if len(os.Args) > 2 {
				// 因为经常会只更新android的版本号，所以在app里传过来，方便在日志里显示
				VERSION = os.Args[2]
			}
			httpme.SetDns("223.6.6.6:53")
			// 全屋同步补偿ms
			app.SendLocal("DSEEK " + conf.Cfg.DefSeek)
		} else {
			player.ShellPlayer = os.Args[1]
		}
	}
	if !conf.IsApp {
		player.InitDefaultShellPlayer()
	}
	// 全局禁用TLS验证 兼容老系统
	httpme.SetSkipVerify(true)

	if !conf.IsApp {
		log.Println("使用音乐播放器：", player.ShellPlayer)
	}
	conf.Init()
	// 自动更新初始化
	update.Init(VERSION, updateRepo())
	if !conf.IsApp {
		// 清理上次更新留下的临时文件和旧版本备份
		update.CleanupOld()
	}
	// 把配置里的 EQ 设置同步到播放链路，默认关闭时不会有任何额外开销
	player.SyncEQFromConf()
	if player.EQActive() {
		log.Println("均衡器已启用")
	}
	if conf.IsApp && conf.Cfg.Wakelock {
		app.SendLocal("WAKELOCK")
	}
	if conf.IsApp && len(conf.Cfg.Alarm) > 0 {
		app.SendLocal("ALARMON")
	}
	// 设置时区
	time.Local = time.FixedZone("UTC+", conf.Cfg.Tz*3600)
	log.Println("工作咩闹钟 v" + VERSION)
	log.Println("当前时区", time.Local, conf.Cfg.Tz)
	conf.WorkDayApi()
	if !conf.IsApp || conf.IsApp && conf.Cfg.Wakelock {
		// Android在有闹钟时有每分钟的定时器，在启动Wakelock时将使用双重定时器保证一定会被调用
		log.Println("内置定时器已启用")
		go timer()
	}
	go shellInput()
	timeJob()
	go checkUpdate()
	run := router.Init("/")
	port := conf.Cfg.Port
	ip, _ := app.GetLocalIP()
	for {
		conf.Port = fmt.Sprintf(":%d", port)
		if conf.IsApp {
			fmt.Println("ECHO 访问http://" + ip + conf.Port)
		}
		fmt.Println("\n使用浏览器访问 http://" + ip + conf.Port + " 进入后台\n")
		// 用 http.Server 是为了 ConnState：播放实时流时要拿 net.Conn
		// 去查内核接收缓冲里还压着多少没读的旧音频，也能顺手收紧这个缓冲
		srv := &http.Server{
			Addr:      conf.Port,
			Handler:   run,
			ConnState: player.PCMStreamConnState,
		}
		if err := srv.ListenAndServe(); err != nil {
			port++
			log.Println("启动失败，端口被占" + conf.Port)
		}
	}
}

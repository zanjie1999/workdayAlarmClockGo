/*
 * 工作咩闹钟 Go
 * zyyme 20230630
 */

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"workdayAlarmClock/alarm"
	"workdayAlarmClock/app"
	"workdayAlarmClock/command"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/nemusic"
	"workdayAlarmClock/player"
	"workdayAlarmClock/router"
	"workdayAlarmClock/update"
	"workdayAlarmClock/weather"
	"workdayAlarmClock/xiaozhi"

	"github.com/gin-gonic/gin"
	"github.com/zanjie1999/httpme"
)

var (
	VERSION  = "53.0"
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

	// LVGL定时获取天气 1小时一次，Android亮屏时自己会请求，不走这里
	if conf.WorkDayApiErr || (conf.IsLvgl && hhmm[2:] == "00") {
		weather.GetWeather("")
	}
	// 如出错则每分钟重试 比如刚开机时间是1970-01-01或是压根没网
	if conf.WorkDayApiErr || hhmm == "0000" {
		conf.WorkDayApi()
	}
	if !conf.IsApp && hhmm == conf.Cfg.WeatherUpdate {
		weather.GetWeather("")
	}
	if hhmm == "2233" {
		// 自动检查更新
		go checkUpdate()
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
				alarm.SendNextWake()
				break
			} else if dayType == mmdd {
				// 月日
				log.Println("闹钟时间到", mmdd, "的", hhmm)
				player.PlayAlarm()
				break
			}
		}
	}
	// 算下一次该醒的时间，通知 Android 壳排精确闹钟
	alarm.SendNextWake()
}

// 处理shell输入 go shellInput()
//
// 这里只留 shell 专属的命令（进程控制、文件操作、连按防抖）。
// 播放器和闹钟的命令全部转给 command.Run，和语音那边走同一份实现。
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
			case "next":
				// 防抖收集连按的next：按一下来一条，停手超过窗口期才一次跳到位
				n := 1
				last := time.Now()
				for time.Since(last) < 200*time.Millisecond {
					buf, _ := reader.Peek(reader.Buffered())
					if i := bytes.IndexByte(buf, '\n'); i >= 0 {
						if strings.TrimSpace(string(buf[:i])) != "next" {
							break // 后面排着别的命令，先跳完这批再处理
						}
						reader.Discard(i + 1)
						n++
						last = time.Now() // 还在连按，窗口往后延
						continue
					}
					time.Sleep(10 * time.Millisecond) // 等用户继续按
				}
				app.Send(player.NextN(n))
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
				app.Send("ECHO " + ip)
				fmt.Println(ip)
			case "weather":
				weather.GetWeather("")
			case "update":
				go checkUpdate()
			default:
				switch {
				case strings.HasPrefix(cmd, "echo "):
					app.Send(cmd[5:])
				case strings.HasPrefix(cmd, "touch "):
					f, e := os.Create(cmd[6:])
					f.Close()
					log.Println(e)
				case strings.HasPrefix(cmd, "rm "):
					log.Println(os.Remove(cmd[3:]))
				case strings.HasPrefix(cmd, "savepath "):
					if cmd[9:] == "null" {
						conf.Cfg.SavePath = ""
					} else {
						conf.Cfg.SavePath = cmd[9:]
					}
					conf.Save()
					fmt.Println("SavePath已修改为", conf.Cfg.SavePath)
				case strings.HasPrefix(cmd, "playlistdl "):
					nemusic.PlaylistDownload(cmd[11:])
				default:
					// 播放器/闹钟命令统一走 command.Run
					if command.Run(cmd) == "" {
						fmt.Println("未知命令", cmd)
					}
				}
			}
			// 丢掉在处理过程中输入的其他命令(比如疯狂的pause会堆积起来)
			// 堆积的next不能丢，要留给防抖统计，连按几下就跳几首
			for reader.Buffered() > 0 {
				buf, _ := reader.Peek(reader.Buffered())
				i := bytes.IndexByte(buf, '\n')
				if i < 0 {
					// 不完整的半行可能是正在写入的next，不动它
					break
				}
				if strings.TrimSpace(string(buf[:i])) == "next" {
					break // 停在第一条next上，交给主循环防抖处理
				}
				reader.Discard(i + 1)
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
	// Apply 成功时进程已经被新版本接管（exec 过去），不会返回
	if err := update.Apply(p); err != nil {
		fmt.Println("更新失败", err)
	}
}

func checkUpdate() {
	for i := 0; i < 3; i++ {
		// 检查更新
		p, err := update.Check()
		if err != nil {
			log.Println("检查更新失败", err)
			time.Sleep(4 * time.Minute)
			continue
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
		// Apply 成功时进程已经被新版本接管（exec 过去），不会返回
		if err := update.Apply(p); err != nil {
			log.Println("自动更新失败", err)
			return
		}
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
	// 从内存接管更新的那一份自己：只负责把新版本写回磁盘再 exec 回去
	if update.HandleInstall(os.Args) {
		return
	}
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
		} else if os.Args[1] == "lvgl" {
			// LVGL 外壳拉起：本地播放 + 往 stdout 上报进度/音量
			// 播放器名顺延到 argv[2]，不传就交给 InitDefaultShellPlayer 自动探测
			conf.IsLvgl = true
			if len(os.Args) > 2 {
				player.ShellPlayer = os.Args[2]
			}
		} else {
			player.ShellPlayer = os.Args[1]
		}
	}
	if !conf.IsApp {
		player.InitDefaultShellPlayer()
	}
	// 把日志输出到stderr
	log.SetOutput(os.Stderr)
	gin.SetMode(gin.ReleaseMode)
	// LVGL的时候不输出gin日志
	if conf.IsLvgl {
		gin.DefaultWriter = io.Discard
	}
	// 全局禁用TLS验证 兼容老系统
	httpme.SetSkipVerify(true)

	if !conf.IsApp {
		log.Println("使用音乐播放器：", player.ShellPlayer)
	}
	conf.Init()
	// 自动更新初始化
	// 版本号给 command 里的状态查询用
	conf.Version = VERSION
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
	// 小智语音助手：配置里开着就后台连上，不阻塞启动
	if !conf.IsApp && conf.Cfg.Xiaozhi.Enabled {
		if err := xiaozhi.Start(xiaozhi.Options{
			Cfg:     conf.Cfg.Xiaozhi,
			Version: VERSION,
		}); err != nil {
			log.Println("小智启动失败", err)
		}
	}
	if conf.AppProto() && conf.Cfg.Wakelock {
		app.SendLocal("WAKELOCK")
	}
	// 设置时区
	time.Local = time.FixedZone("UTC+", conf.Cfg.Tz*3600)
	log.Println("工作咩闹钟 v" + VERSION)
	log.Println("当前时区", time.Local, conf.Cfg.Tz)
	conf.WorkDayApi()
	if conf.AppProto() {
		weather.GetWeather("")
	}
	if !conf.IsApp || conf.IsApp && conf.Cfg.Wakelock {
		// Android 不开 Wakelock 时靠壳的精确闹钟唤醒，不需要内置定时器
		// 开了 Wakelock 才有常驻 CPU 锁，用内置定时器兜底
		log.Println("内置定时器已启用")
		go timer()
	}
	go shellInput()
	// LVGL 模式：每秒往 stdout 报 POS/DUR/VOL（内部自己判断 IsLvgl）
	player.StartReporter()
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
		// 人类可读的提示走 stderr，别污染 stdout 的事件流
		log.Println("使用浏览器访问 http://" + ip + conf.Port + " 进入后台")
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

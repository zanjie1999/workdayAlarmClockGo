/*
 * 播放器
 * zyyme 20230630
 * v1.0
 */

package player

import (
	"log"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/nemusic"
	"workdayAlarmClock/weather"

	"github.com/zanjie1999/httpme"
)

var (
	// 是否停止播放
	IsStop = true
	// 当前的播放列表
	PlayList      = []string{}
	IsAlarm       = false
	IsPlayWeather = false
	UnixCmd       *exec.Cmd
	NowUrl        = ""
	PrevUrl       = ""
	NowId         = ""
	// 开始播放和定时结束时间
	StartUnix   int64 = 0
	StopUnix    int64 = 0
	ShellPlayer       = "play"
	PrevRdmFlag       = false
	SkipAlarm         = 0
)

// 上一首 或一键者播放指定歌单
// 第一次按上一首键会放上一曲（如果有)，第二次会顺序播放歌单，第三次会随机播放歌单（如果没有放完一首）
func Prev() string {
	if PrevUrl != "" {
		PrevRdmFlag = false
	}
	if PrevRdmFlag {
		app.Send("ECHO 随机播放列表")
		PrevRdmFlag = false
		NowUrl = ""
		PrevUrl = ""
		NowId = ""
		// PlayPlaylist(conf.Cfg.DefPlayListId, true)
		// return "随机播放歌单" + conf.Cfg.DefPlayListId
		// 不重新获取 直接随机当前播放列表
		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(PlayList), func(i, j int) {
			PlayList[i], PlayList[j] = PlayList[j], PlayList[i]
		})
		Next()
		return "随机播放列表"
	} else if PrevUrl != "" {
		app.Send("ECHO 上一首")
		PlayList = append([]string{NowUrl}, PlayList...)
		// 不清空的话会永远在这一首和上一首循环 变相清空PrevUrl
		NowUrl = ""
		NowId = ""
		PlayUrl(PrevUrl)
		return "上一首"
	} else {
		app.Send("ECHO 播放歌单" + conf.Cfg.DefPlayListId)
		// 不清空就会在播放歌单和上一首之间循环
		NowUrl = ""
		PrevUrl = ""
		NowId = ""
		if strings.HasPrefix(conf.Cfg.DefPlayListId, "http") {
			PlayUrl(conf.Cfg.DefPlayListId)
			return "播放默认URL" + conf.Cfg.DefPlayListId
		} else {
			PlayPlaylist(conf.Cfg.DefPlayListId, false)
			return "播放默认歌单" + conf.Cfg.DefPlayListId
		}
	}
}

// 下一首
func Next() string {
	for {
		if IsAlarm && NowId != "" {
			// 保存闹钟放过的记录
			log.Println("闹钟记录", NowId)
			conf.Cfg.NePlayed = append(conf.Cfg.NePlayed, NowId)
		}
		if StopUnix != 0 && StopUnix < time.Now().Unix() {
			StopUnix = 0
			log.Println("定时停止")
			Stop()
			return "定时停止"
		}
		if len(PlayList) > 0 {
			now := PlayList[0]
			PlayList = PlayList[1:]
			if len(PlayList) > 0 {
				app.Send("ECHO 待播放 " + strconv.Itoa(len(PlayList)))
			} else {
				app.Send("ECHO 正在播放")
			}
			if len(now) > 3 && now[:4] == "http" {
				PlayUrl(now)
				return now
			} else {
				NowId = now
				u := nemusic.MusicUrl(now)
				if u != "" {
					PlayUrl(u)
					return u
				}
			}
		} else {
			Stop()
			log.Println("停止播放")
			return "停止播放"
		}
	}
}

// 一键急停按钮 自动控制播放停止
func Me1Key() string {
	if IsStop {
		PlayPlaylist(conf.Cfg.DefPlayListId, false)
		return "play"
	} else {
		Stop()
		return "stop"
	}
}

// 播放歌单
func PlayPlaylist(id string, random bool) {
	// 在播放任意歌单后，按上一首来随机
	PrevRdmFlag = true
	ids := nemusic.PlayList(id)
	if random {
		// 打乱歌单
		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(ids), func(i, j int) {
			ids[i], ids[j] = ids[j], ids[i]
		})
	}
	PlayList = ids
	Next()
}

// 预下载播报的天气
func DownWeather() {
	os.Remove("weather.mp3")
	msg := weather.GetWeather("")
	if msg != "" {
		downloadFile("https://dds.dui.ai/runtime/v1/synthesize?voiceId=cyangfp&speed=1&volume=100&audioType=mp3&text="+url.QueryEscape(msg), "weather.mp3")
	}
}

// 播放url音乐
func PlayUrl(url string) {
	if StartUnix == 0 {
		StartUnix = time.Now().Unix()
		if conf.Cfg.MuteWhenStop && !IsPlayWeather && !IsAlarm {
			SetVol(conf.Cfg.VolDefault)
		}
	}
	IsStop = false
	PrevUrl = NowUrl
	NowUrl = url
	if conf.IsApp {
		app.PlayUrl(url)
	} else {
		go UnixPlayUrl(url)
	}
}

func Stop() {
	app.Send("ECHO 工作咩闹钟")
	PrevRdmFlag = false
	PrevUrl = NowUrl
	NowUrl = ""
	StartUnix = 0
	StopUnix = 0
	// 保存闹钟播放记录
	if IsAlarm && NowId != "" {
		if len(conf.Cfg.NePlayed) < 1 || conf.Cfg.NePlayed[len(conf.Cfg.NePlayed)-1] != NowId {
			conf.Cfg.NePlayed = append(conf.Cfg.NePlayed, NowId)
		}
		log.Println("闹钟保存，已播放", len(conf.Cfg.NePlayed))
		conf.Save()
	}
	NowId = ""
	PlayList = []string{}
	if conf.IsApp {
		app.Send("STOP")
	} else {
		// exec.Command("killall", "play").Run()
		if UnixCmd != nil {
			log.Println(UnixCmd.Process.Signal(syscall.SIGINT))
			log.Println(UnixCmd.Process.Kill())
		}
	}
	if IsAlarm {
		IsAlarm = false
		IsPlayWeather = true
		// 结束闹钟时播放天气
		if stat, err := os.Stat("weather.mp3"); err == nil && stat.Size() > 0 {
			if conf.IsApp {
				PlayUrl("./weather.mp3")
			} else {
				PlayUrl("http://127.0.0.1:8080/weather.mp3")
			}
		}
	} else if IsPlayWeather {
		IsPlayWeather = false
		if conf.Cfg.MuteWhenStop {
			SetVol("0")
		} else {
			SetVol(conf.Cfg.VolDefault)
		}
		PrevUrl = ""
		if conf.IsApp {
			app.Send("SCREENOFF")
		}
	} else if conf.Cfg.MuteWhenStop {
		SetVol("0")
	}
	IsStop = true
}

// 设置音量
func SetVol(per string) {
	log.Println("设置音量", per, "%")
	if conf.IsApp {
		app.Send("VOL " + per)
	}
}

// 去重
func filterList(in, filter []string) []string {
	filterMap := make(map[string]struct{})
	for _, id := range filter {
		filterMap[id] = struct{}{}
	}
	var out []string
	for _, id := range in {
		if _, exists := filterMap[id]; !exists {
			out = append(out, id)
		}
	}
	return out
}

// 播放闹钟音乐 时间到时调用
func PlayAlarm() {
	app.Send("ECHO 闹钟")
	if SkipAlarm > 0 {
		SkipAlarm--
		log.Println("跳过闹钟")
		return
	}
	IsAlarm = true
	if conf.IsApp {
		app.Send("ALARM")
	}
	SetVol(conf.Cfg.VolAlarm)
	// 预下载天气信息
	go DownWeather()
	PlayList = []string{}
	if strings.HasPrefix(conf.Cfg.NePlayListId, "http") {
		log.Println("闹钟歌单配置的是URL，播放", conf.Cfg.NePlayListId)
		PlayUrl(conf.Cfg.NePlayListId)
		return
	}
	ids := nemusic.PlayList(conf.Cfg.NePlayListId)
	// 定时停止闹钟
	StopUnix = time.Now().Unix() + int64(conf.Cfg.AlarmTime*60)
	if len(ids) == 0 {
		// 兜底
		log.Println("获取不到歌单，播放默认歌曲")
		PlayUrl("http://127.0.0.1:8080/music.mp3")
		return
	} else {
		// 放完一次了 重置
		if len(conf.Cfg.NePlayed)+1 >= len(ids) {
			log.Println("闹钟歌单，共", len(ids), "，重置已播放")
			conf.Cfg.NePlayed = []string{}
		} else {
			log.Println("闹钟歌单，共", len(ids), "，已播放", len(conf.Cfg.NePlayed))
			ids = filterList(ids, conf.Cfg.NePlayed)
		}
		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(ids), func(i, j int) {
			ids[i], ids[j] = ids[j], ids[i]
		})
		PlayList = ids
		if len(PlayList) == 0 {
			// 你该不是在拿咩咩寻开心吧
			log.Println("这歌单没一首能放的！你该不是在拿咩咩寻开心吧！")
			PlayList = append(PlayList, "http://127.0.0.1:8080/music.mp3")
		}
		conf.Save()
		Next()
	}
}

// 下载文件 细想一下之前为什么之前要写个curl，直接用http咩不更好
func downloadFile(url string, filename string) error {
	resp, err := httpme.Get(url)
	if err != nil {
		return err
	}
	resp.SaveFile(filename)
	return nil
}

// beep库 win 和linux alsa可以用 android不行
// func BeepPlayUrl(url string) {
// 	request, err := http.NewRequest("GET", url, nil)
// 	if err != nil {
// 		log.Println(err)
// 	}
// 	client := &http.Client{}
// 	response, err := client.Do(request)
// 	if err != nil {
// 		log.Println(err)
// 	}
// 	defer response.Body.Close()
// 	// 读取文件流播放
// 	streamer, format, err := mp3.Decode(response.Body)
// 	if err != nil {
// 		log.Println(err)
// 	}
// 	defer streamer.Close()
// 	speaker.Init(format.SampleRate, format.SampleRate.N(time.Second/10))
// 	app.Send("music length :", streamer.Len())
// 	speaker.Play(streamer)
// 	select {}
// }

// Linux: apt install sox
// macOS: brew install sox
// Win和Linux推荐使用meMp3Player
func UnixPlayUrl(url string) {
	log.Println("start play:" + url)
	if UnixCmd != nil {
		// 需要先kill掉之前的
		log.Println(UnixCmd.Process.Signal(syscall.SIGINT))
		log.Println(UnixCmd.Process.Kill())
	}
	pwd, _ := os.Getwd()
	var err error
	if strings.Contains(ShellPlayer, "meMp3Player") {
		// 使用 咩MP3播放器 的时候，无需下载，直接播放音频流
		log.Println("shell: ", ShellPlayer, url)
		UnixCmd = exec.Command(ShellPlayer, url)
	} else {
		err = downloadFile(url, pwd+"/play.mp3")
		if err != nil {
			log.Println("download error:" + err.Error())
			return
		}

		log.Println("shell: ", ShellPlayer, pwd+"/play.mp3")
		UnixCmd = exec.Command(ShellPlayer, pwd+"/play.mp3")
	}
	err = UnixCmd.Start()
	if err != nil {
		log.Println("run " + ShellPlayer + " error:" + err.Error())
		return
	}
	err = UnixCmd.Wait()
	if err != nil {
		log.Println("wait", ShellPlayer, " error:"+err.Error())
		return
	}
	if !IsStop && NowUrl == url || IsPlayWeather {
		// 相等说明不是被外部中断是放完了或者类似mac的open那样不阻塞的
		// time.Sleep(time.Second)
		// os.Remove("play.mp3")
		// // err = os.Remove("play.mp3")
		// // for err != nil {
		// // 	// 文件被占用即为正在播放
		// // 	time.Sleep(time.Second)
		// // 	err = os.Remove("play.mp3")
		// // }
		// 不删吧留着吧不差这点存储空间
		log.Println("end play:" + url)
		Next()
	}
}

// 不太行 于是写了个app
// func AndroidPlayUrl(url string) {
// 	cmd := exec.Command("curl -L -k " + url + " > /sdcard/1.mp3")
// 	err := cmd.Start()
// 	if err != nil {
// 		log.Println("run curl error:" + err.Error())
// 		// return
// 	}
// 	err = cmd.Wait()
// 	if err != nil {
// 		log.Println("wait curl error:" + err.Error())
// 		// return
// 	}
// 	cmd = exec.Command("am start -a android.intent.action.VIEW -t audio/mp3 -d \"file:///sdcard/1.mp3\"")
// 	err = cmd.Start()
// 	if err != nil {
// 		log.Println("run am error:" + err.Error())
// 		return
// 	}
// 	err = cmd.Wait()
// 	if err != nil {
// 		log.Println("wait am error:" + err.Error())
// 		return
// 	}
// }

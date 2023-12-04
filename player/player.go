/*
 * 播放器
 * zyyme 20230630
 * v1.0
 */

package player

import (
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"syscall"
	"time"
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
	LastUrl       = ""
	ShellPlayer   = "play"
)

// 下一首
func Next() string {
	for {
		if len(PlayList) > 0 {
			now := PlayList[0]
			PlayList = PlayList[1:]
			if len(now) > 3 && now[:4] == "http" {
				PlayUrl(now)
				return now
			} else {
				u := nemusic.MusicUrl(now)
				if u != "" {
					PlayUrl(u)
					return u
				}
			}
		} else {
			Stop()
			return "stop"
		}
	}
}

// 播放歌单
func PlayPlaylist(id string, random bool) {
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

// 预下载播报的天气 请不要拿api滥用谢谢
func DownWeather() {
	msg := weather.GetWeather("")
	if msg != "" {
		downloadFile("https://tts.zyym.eu.org/m="+url.QueryEscape(msg), "weather.mp3")
	}
}

// 播放url音乐
func PlayUrl(url string) {
	IsStop = false
	LastUrl = url
	if conf.IsApp {
		fmt.Println("PLAY " + url)
	} else {
		go UnixPlayUrl(url)
	}
}

func Stop() {
	// 如果还有没放完的闹钟就被掐掉了 那么把那首还回去下次继续抽
	if IsAlarm && len(PlayList) > 0 {
		conf.Cfg.NePlayed = conf.Cfg.NePlayed[len(PlayList):]
		conf.Save()
	}
	PlayList = []string{}
	if conf.IsApp {
		fmt.Println("STOP")
	} else {
		// exec.Command("killall", "play").Run()
		if UnixCmd != nil {
			log.Println(UnixCmd.Process.Signal(syscall.SIGINT))
		}
	}
	if IsAlarm {
		IsAlarm = false
		IsPlayWeather = true
		// 结束闹钟时播放天气
		PlayUrl("http://127.0.0.1:8080/weather.mp3")
	} else if IsPlayWeather {
		IsPlayWeather = false
		if conf.IsApp {
			fmt.Println("VOL " + conf.Cfg.VolDefault)
		}
	}
	IsStop = true
}

// 播放闹钟音乐 时间到时调用
func PlayAlarm() {
	IsAlarm = true
	if conf.IsApp {
		fmt.Println("VOL " + conf.Cfg.VolAlarm)
	}
	PlayList = []string{}
	ids := nemusic.PlayList(conf.Cfg.NePlayListId)
	// 预下载天气信息
	go DownWeather()
	if len(ids) == 0 {
		// 兜底
		log.Println("获取不到歌单，播放默认歌曲")
		PlayUrl("http://127.0.0.1:8080/music.mp3")
		return
	} else {
		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(ids), func(i, j int) {
			ids[i], ids[j] = ids[j], ids[i]
		})
		for _, id := range ids {
			// 放完一次了 重置
			if len(conf.Cfg.NePlayed)+1 >= len(ids) {
				conf.Cfg.NePlayed = []string{}
			}
			if len(PlayList) == 2 {
				break
			}
			// 检查是否播放过
			flag := true
			for i := 0; i < len(conf.Cfg.NePlayed); i++ {
				if conf.Cfg.NePlayed[i] == id {
					flag = false
					break
				}
			}
			if flag {
				conf.Cfg.NePlayed = append(conf.Cfg.NePlayed, id)
				// 检查是否能放 没问题就放进去
				if nemusic.MusicUrl(id) != "" {
					PlayList = append(PlayList, id)
				}
			}
		}
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
// 	fmt.Println("music length :", streamer.Len())
// 	speaker.Play(streamer)
// 	select {}
// }

// Linux: apt install sox
// macOS: brew install sox
func UnixPlayUrl(url string) {
	log.Println("start play:" + url)
	if UnixCmd != nil {
		// 需要先kill掉之前的
		UnixCmd.Process.Signal(syscall.SIGINT)
	}
	pwd, _ := os.Getwd()
	err := downloadFile(url, pwd+"/play.mp3")
	if err != nil {
		log.Println("download error:" + err.Error())
		return
	}

	log.Println("shell: ", ShellPlayer, pwd+"/play.mp3")
	UnixCmd = exec.Command(ShellPlayer, pwd+"/play.mp3")
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
	if !IsStop && LastUrl == url {
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

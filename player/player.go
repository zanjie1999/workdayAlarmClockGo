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
	"os/exec"
	"time"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/nemusic"
)

var (
	// 是否停止播放
	IsStop = true
	// 当前的播放列表
	PlayList = []string{}
	IsAlarm  = false
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

// 播放url音乐
func PlayUrl(url string) {
	IsStop = false
	if conf.IsApp {
		fmt.Println("PLAY " + url)
	} else {
		LinuxPlayUrl(url)
	}
}

func Stop() {
	PlayList = []string{}
	if conf.IsApp {
		fmt.Println("STOP")
		if IsAlarm {
			IsAlarm = false
			fmt.Println("VOL " + conf.Cfg.VolDefault)
		}
	} else {
		IsStop = true
		exec.Command("killall", "play").Run()
	}
}

// 播放闹钟音乐 时间到时调用
func PlayAlarm() {
	IsAlarm = true
	if conf.IsApp {
		fmt.Println("VOL " + conf.Cfg.VolAlarm)
	}
	PlayList = []string{}
	ids := nemusic.PlayList(conf.Cfg.NePlayListId)
	if len(ids) == 0 {
		// 兜底
		log.Println("获取不到歌单，播放默认歌曲")
		PlayUrl("http://127.0.0.1:8080/static/music.mp3")
		return
	} else {
		rand.Seed(time.Now().UnixNano())
		rand.Shuffle(len(ids), func(i, j int) {
			ids[i], ids[j] = ids[j], ids[i]
		})
		for _, id := range ids {
			if len(conf.Cfg.NePlayed)+2 >= len(ids) {
				conf.Cfg.NePlayed = []string{}
			}
			if len(PlayList) == 2 {
				break
			}
			flag := true
			for i := 0; i < len(conf.Cfg.NePlayed); i++ {
				if conf.Cfg.NePlayed[i] == id {
					flag = false
					break
				}
			}
			if flag {
				conf.Cfg.NePlayed = append(conf.Cfg.NePlayed, id)
				PlayList = append(PlayList, id)
			}

		}
		conf.Save()
		Next()
	}
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

// apt install sox
func LinuxPlayUrl(url string) {
	cmd := exec.Command("curl -L -k " + url + " > 1.mp3")
	err := cmd.Start()
	if err != nil {
		log.Println("run curl error:" + err.Error())
		// return
	}
	err = cmd.Wait()
	if err != nil {
		log.Println("wait curl error:" + err.Error())
		// return
	}

	cmd = exec.Command("play 1.mp3")
	err = cmd.Start()
	if err != nil {
		log.Println("run sox play error:" + err.Error())
		return
	}
	err = cmd.Wait()
	if err != nil {
		log.Println("wait sox play error:" + err.Error())
		return
	}

	cmd = exec.Command("play 1.mp3")
	err = cmd.Start()
	if err != nil {
		log.Println("run rm error:" + err.Error())
		return
	}
	err = cmd.Wait()
	if err != nil {
		log.Println("wait rm error:" + err.Error())
		return
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

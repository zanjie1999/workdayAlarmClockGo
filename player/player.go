package player

import (
	"io"
	"log"
	"math/rand"
	"net/http"
	"os/exec"
	"time"
	"workdayAlarmClock/nemusic"
)

// 播放url音乐
func PlayUrl(url string) {
	// resp, err := requests.Get(url)
	// if err != nil {
	// 	log.Println(err)
	// }
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Println(err)
	}
	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		log.Println(err)
	}
	defer response.Body.Close()
	// 读取文件流播放
	PlayMp3(response.Body)
}

// 播放闹钟音乐 时间到时调用
func PlayAlarm() {

}

func PlayMp3(fileStream io.ReadCloser) {
	// streamer, format, err := mp3.Decode(fileStream)
	// if err != nil {
	// 	log.Println(err)
	// }
	// defer streamer.Close()

	// speaker.Init(format.SampleRate, format.SampleRate.N(time.Second/10))
	// fmt.Println("music length :", streamer.Len())
	// speaker.Play(streamer)
	// select {}
}

func AndroidPlayUrl(url string) {
	cmd := exec.Command("curl -k " + url + " > /sdcard/1.mp3")
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

	cmd = exec.Command("am start -a android.intent.action.VIEW -t audio/mp3 -d \"file:///sdcard/1.mp3\"")
	err = cmd.Start()
	if err != nil {
		log.Println("run am error:" + err.Error())
		return
	}
	err = cmd.Wait()
	if err != nil {
		log.Println("wait am error:" + err.Error())
		return
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
	for _, v := range ids {
		url := nemusic.MusicUrl(v)
		if url != "" {
			PlayUrl(url)
		}
	}

}

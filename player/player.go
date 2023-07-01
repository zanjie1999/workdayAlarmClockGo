package player

import (
	"math/rand"
	"time"
	"workdayAlarmClock/nemusic"
)

// 播放url音乐
func PlayUrl(url string) {

}

// 播放闹钟音乐 时间到时调用
func PlayAlarm() {

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

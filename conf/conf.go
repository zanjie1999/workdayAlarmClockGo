/*
 * 配置 使用json存储减少依赖
 * zyyme 20230704
 * v1.0
 */

package conf

import (
	"encoding/json"
	"log"
	"os"
)

type Config struct {
	VolDefault      string              `json:"volDefault"`
	VolAlarm        string              `json:"volAlarm"`
	Alarm           map[string][]string `json:"alarm"`
	NePlayListId    string              `json:"nePlayListId"`
	DefPlayListId   string              `json:"defPlayListId"`
	NePlayed        []string            `json:"nePlayed"`
	Tz              int                 `json:"tz"`
	WeatherCityCode string              `json:"weatherCityCode"`
	Today           string              `json:"today"`
	TodayFc         int                 `json:"todayFc"`
	TodayFd         int                 `json:"todayFd"`
	Lastday         string              `json:"lastday"`
	LastdayFc       int                 `json:"lastdayFc"`
	LastdayFd       int                 `json:"lastdayFd"`
	WeatherUpdate   string              `json:"weatherUpdate"`
	Wakelock        bool                `json:"wakelock"`
	AlarmTime       float64             `json:"alarmTime"`
	MuteWhenStop    bool                `json:"muteWhenStop"`
}

var (
	// 今天是工作日吗
	IsWorkDay = false
	// 配合Android App使用
	IsApp = false

	// 配置
	Cfg = Config{
		// 闹钟时间 24小时制hhmm 工作日1 休息日2 一次性3 每天4
		Alarm: map[string][]string{},
		// 闹钟歌单
		NePlayListId: "2236121100",
		// 按上一曲时默认歌单
		DefPlayListId: "21777546",
		// 已经播放过的歌曲
		NePlayed: []string{},
		// 闹钟音量
		VolAlarm: "80",
		// 默认音量
		VolDefault: "50",
		// 时区
		Tz: 8,
		// 默认更新天气的时间
		WeatherUpdate: "0700",
		// Android唤醒锁开
		Wakelock: true,
		// 闹钟时长
		AlarmTime: 4.5,
		// 停止时静音
		MuteWhenStop: false,
	}
)

// 加载配置
func Init() {
	if _, err := os.Stat("workdayAlarmClock.json"); err != nil {
		log.Println("配置文件不存在，创建配置文件")
		Save()
	} else {
		f, err := os.Open("workdayAlarmClock.json")
		if err != nil {
			log.Println("配置文件打开失败", err)
			Save()
		} else {
			defer f.Close()
			decoder := json.NewDecoder(f)
			err = decoder.Decode(&Cfg)
			if err != nil {
				log.Println("配置文件解析失败", err)
				Save()
			}
		}
	}
}

// 保存配置
func Save() {
	f, err := os.Create("workdayAlarmClock.json")
	if err != nil {
		log.Println("配置文件创建失败", err)
		return
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	err = encoder.Encode(Cfg)
	if err != nil {
		log.Println("配置文件写入失败", err)
	}
}

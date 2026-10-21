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
	"time"

	"github.com/zanjie1999/httpme"
)

// EQBand 一个滤波段
type EQBand struct {
	// Type: "lowshelf" | "highshelf" | "peaking"
	Type string `json:"type"`
	// Freq 中心/拐点频率 Hz
	Freq float64 `json:"freq"`
	// Gain 增益 dB，正为增强负为衰减
	Gain float64 `json:"gain"`
	// Q 品质因数，0 时用默认 0.707
	Q float64 `json:"q"`
}

// EQSettings 均衡器配置，对应 json 里的 eq 字段。
// 默认关闭，enabled 为 false 时播放链路完全跳过处理。
type EQSettings struct {
	Enabled bool     `json:"enabled"`
	Bands   []EQBand `json:"bands"`
	// Preamp 预增益 dB，用来补偿增强后的削波
	Preamp float64 `json:"preamp"`
}

// 三段均衡器的固定频点：低音 / 中音 / 高音
const (
	EQBassFreq    = 120  // 低音拐点
	EQMidFreq     = 1000 // 中音中心
	EQTrebleFreq  = 8000 // 高音拐点
	EQMidDefaultQ = 1.0
)

// DefaultEQ 默认配置：三段均衡器，关闭状态。
// 频点顺序固定为 低音、中音、高音，eq.html 按这个顺序渲染。
func DefaultEQ() EQSettings {
	return EQSettings{
		Enabled: false,
		Preamp:  0,
		Bands: []EQBand{
			{Type: "lowshelf", Freq: EQBassFreq, Gain: 0, Q: 0.707},
			{Type: "peaking", Freq: EQMidFreq, Gain: 0, Q: EQMidDefaultQ},
			{Type: "highshelf", Freq: EQTrebleFreq, Gain: 0, Q: 0.707},
		},
	}
}

// NormalizeEQ 把配置整理成标准的三段结构。
// 老配置文件里可能只有一段(低音)，或者频段类型对不上，
// 这里统一补齐成 低音/中音/高音，保留已有的增益值。
func NormalizeEQ(eq EQSettings) EQSettings {
	def := DefaultEQ()
	out := EQSettings{Enabled: eq.Enabled, Preamp: eq.Preamp, Bands: def.Bands}

	// 按类型把已有频段的增益搬到对应位置
	for _, band := range eq.Bands {
		switch band.Type {
		case "lowshelf":
			out.Bands[0].Gain = band.Gain
			if band.Freq > 0 {
				out.Bands[0].Freq = band.Freq
			}
		case "peaking":
			out.Bands[1].Gain = band.Gain
			if band.Freq > 0 {
				out.Bands[1].Freq = band.Freq
			}
			if band.Q > 0 {
				out.Bands[1].Q = band.Q
			}
		case "highshelf":
			out.Bands[2].Gain = band.Gain
			if band.Freq > 0 {
				out.Bands[2].Freq = band.Freq
			}
		}
	}
	return out
}

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
	MusicQuality    string              `json:"musicQuality"`
	SavePath        string              `json:"savePath"`
	CacheLimit      int64               `json:"cacheLimit"`
	BroadcastMode   bool                `json:"broadcastMode"`
	DefSeek         string              `json:"defSeek"`
	SmallWeekDate   string              `json:"smallWeekDate"`
	Port            int                 `json:"port"`
	// 均衡器配置，默认关闭
	Eq EQSettings `json:"eq"`
}

var (
	// 今天是工作日吗
	IsWorkDay = false
	// 配合Android App使用
	IsApp = false
	// 获取工作日信息出错
	WorkDayApiErr = false
	// 实际端口
	Port = ":8080"

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
		Wakelock: false,
		// 闹钟时长
		AlarmTime: 4.5,
		// 停止时静音
		MuteWhenStop: false,
		// 音质
		MusicQuality: "standard",
		// 保存音乐文件缓存
		SavePath: "",
		// 音乐缓存上限（MB）
		CacheLimit: 1024,
		// udp广播群控模式
		BroadcastMode: false,
		// 全屋同步补偿ms
		DefSeek: "0",
		// 下一次小周日期，双休时为空
		SmallWeekDate: "",
		// 端口号
		Port: 8080,
		// 均衡器，默认关闭
		Eq: DefaultEQ(),
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
	if Cfg.CacheLimit <= 0 {
		Cfg.CacheLimit = 1024
	}
	// 老配置文件没有 eq 字段或只有一段，统一整理成三段
	Cfg.Eq = NormalizeEQ(Cfg.Eq)
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

// 获取今天是不是工作日
func WorkDayApi() {
	log.Println("正在获取工作日状态，如时间很长请检查网络")
	req := httpme.Httpme()
	yymmdd := time.Now().Format("2006-01-02")
	if yymmdd == "1970-01-01" {
		log.Println("等待时间同步再获取工作日信息")
		WorkDayApiErr = true
		return
	}
	resp, err := req.Get("https://timor.tech/api/holiday/info/" + yymmdd)
	if err == nil {
		var j map[string]interface{}
		resp.Json(&j)
		if j["code"].(float64) == 0 {
			t := j["type"].(map[string]interface{})["type"].(float64)
			IsWorkDay = t == 0 || t == 3
			if t == 0 {
				log.Println("普通工作日")
			} else if t == 1 {
				log.Println("普通周六日")
			} else if t == 2 {
				log.Println("法定节假日")
			} else if t == 3 {
				log.Println("调休补班")
			}
			log.Println(j["type"].(map[string]interface{})["name"], "工作日吗？", IsWorkDay)
			WorkDayApiErr = false
		} else {
			WorkDayApiErr = true
			log.Println("获取工作日信息出错", resp)
			IsWorkDay = time.Now().Weekday() != time.Saturday && time.Now().Weekday() != time.Sunday
		}
	} else {
		WorkDayApiErr = true
		log.Println("获取工作日信息出错", err)
		IsWorkDay = time.Now().Weekday() != time.Saturday && time.Now().Weekday() != time.Sunday
	}

	// 大小周 小周当天强制工作日
	if Cfg.SmallWeekDate != "" {
		// 时间已经过了，计算下一个小周日期
		smallWeekDate, err := time.Parse("20060102", Cfg.SmallWeekDate)
		if err == nil {
			if smallWeekDate.Before(time.Now()) {
				// 先+7 这是大周 双休
				smallWeekDate = smallWeekDate.AddDate(0, 0, 7)
				for {
					smallWeekDate = smallWeekDate.AddDate(0, 0, 7)
					if smallWeekDate.Before(time.Now()) {
						continue
					}
					yymmdd := smallWeekDate.Format("2006-01-02")
					resp, err := req.Get("https://timor.tech/api/holiday/info/" + yymmdd)
					if err == nil {
						var j map[string]interface{}
						resp.Json(&j)
						if j["code"].(float64) == 0 {
							// +7直到他不是法定节假日
							t := j["type"].(map[string]interface{})["type"].(float64)
							if t == 2 {
								log.Println(yymmdd + "是法定节假日，休息")
							} else if t == 3 {
								log.Println(yymmdd + "是调休补班，工作")
							} else {
								Cfg.SmallWeekDate = smallWeekDate.Format("20060102")
								log.Println("下一个小周日期是", Cfg.SmallWeekDate)
								Save()
								break
							}
						} else {
							log.Println("获取工作日信息出错", resp, "取7天后")
							break
						}
					} else {
						log.Println("获取工作日信息出错", err, "取7天后")
						break
					}
				}
			}
		} else {
			log.Println("小周日期格式错误，已清空", err)
			Cfg.SmallWeekDate = ""
		}

		if time.Now().Format("20060102") == Cfg.SmallWeekDate {
			IsWorkDay = true
			log.Println("根据大小周设置，今天是小周工作日")
		}
	}
}

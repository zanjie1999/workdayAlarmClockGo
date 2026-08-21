/*
 * 天气
 * zyyme 20231120
 * v1.0
 */

package weather

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"workdayAlarmClock/conf"

	"github.com/zanjie1999/httpme"
)

func GetCityCode(q string) (string, string, error) {
	if q != "" {
		resp, err := httpme.Get("https://toy1.weather.com.cn/search", httpme.Params{"cityname": q}, httpme.Header{"Referer": "http://www.weather.com.cn/"})
		if err != nil {
			log.Println("GetCityCode 请求出错", err)
			return "", "", err
		}
		var j []map[string]string
		t := resp.Text()
		log.Println("GetCityCode:", t)
		err = json.Unmarshal([]byte(t[1:len(t)-1]), &j)
		if err != nil {
			log.Println("GetCityCode json解析出错", err)
			return "", "", err
		}
		if len(j) > 0 {
			return strings.Split(j[0]["ref"], "~")[0], j[0]["ref"], nil
		}
	}
	return "", "", errors.New("没有结果")
}

func GetWeatherApi(code string) (map[string]string, map[string]string, string, string, error) {
	if code != "" {
		resp, err := httpme.Get("http://d1.weather.com.cn/weather_index/"+code+".html", httpme.Header{"Referer": "http://www.weather.com.cn/html/weather/" + code + ".html"})
		if err != nil {
			log.Println("GetWeather 请求出错", err)
			return nil, nil, "", "", err
		}
		if resp.R.Request.URL.Path == "/other/weather_error_404.html" {
			return nil, nil, "", "", errors.New("code错误")
		}

		// 如果code不对会炸无法判断
		str := resp.Text()

		// 今天天气
		// indexStart := strings.Index(str, "weatherinfo\":")
		// indexEnd := strings.Index(str, "};var alarmDZ")
		// jsonCityDZ := str[indexStart+13 : indexEnd]
		// fmt.Println("GetWeather weatherinfo", jsonCityDZ)

		// 20260819当天天气文字描述没了(比如晴转多云)
		// indexStart := strings.Index(str, "\"weather\":\"")
		// indexEnd := strings.Index(str, "\",\"wd\"")
		// weather := str[indexStart+11 : indexEnd]

		// 预警
		indexStart := strings.Index(str, "\"w9\":\"")
		indexEnd := strings.Index(str, "\",\"w10\"")
		alarm := ""
		if indexStart != -1 && indexEnd != -1 {
			alarm = str[indexStart+6 : indexEnd]
		}
		log.Println("GetWeather alarm", alarm)

		// 名字 当前气温 天气 日期
		indexStart = strings.Index(str, "dataSK =")
		indexEnd = strings.Index(str, ";var dataZS")
		jsonDataSK := str[indexStart+8 : indexEnd]
		log.Println("GetWeather sk", jsonDataSK)

		// 5天预报只取今天
		indexStart = strings.Index(str, "\"f\":[")
		// jsonFC := str[indexStart+4 : len(str)-1]
		indexEnd = strings.Index(str, ",{\"fa")
		jsonFC := str[indexStart+5 : indexEnd]
		log.Println("GetWeather fc", jsonFC)

		// 图标转文字
		weatherMap := map[string]string{
			"00": "晴",
			"01": "多云",
			"02": "阴",
			"03": "阵雨",
			"04": "雷阵雨",
			"05": "雷阵雨伴有冰雹",
			"06": "雨夹雪",
			"07": "小雨",
			"08": "中雨",
			"09": "大雨",
			"10": "暴雨",
			"11": "大暴雨",
			"12": "特大暴雨",
			"13": "阵雪",
			"14": "小雪",
			"15": "中雪",
			"16": "大雪",
			"17": "暴雪",
			"18": "雾",
			"19": "冻雨",
			"20": "沙尘暴",
			"21": "小到中雨",
			"22": "中到大雨",
			"23": "大到暴雨",
			"24": "暴雨到大暴雨",
			"25": "大暴雨到特大暴雨",
			"26": "小到中雪",
			"27": "中到大雪",
			"28": "大到暴雪",
			"29": "浮尘",
			"30": "扬沙",
			"31": "强沙尘暴",
			"32": "霾",
		}
		fa := jsonFC[7:9]
		fb := jsonFC[17:19]
		var weather string
		if fa == fb {
			weather = weatherMap[fa]
		} else {
			weather = weatherMap[fa] + "转" + weatherMap[fb]
		}
		log.Println("GetWeather weather", weather)

		var sk, fc map[string]string
		err = json.Unmarshal([]byte(jsonFC), &fc)
		if err != nil {
			return sk, fc, weather, alarm, err
		}
		err = json.Unmarshal([]byte(jsonDataSK), &sk)
		if err != nil {
			return sk, fc, weather, alarm, err
		}
		return sk, fc, weather, alarm, nil
	}
	return nil, nil, "", "", errors.New("没有code查什么？")
}

func GetWeather(code string) string {
	if code == "" {
		code = conf.Cfg.WeatherCityCode
	}
	if code != "" {
		sk, fc, weather, alarm, err := GetWeatherApi(code)
		if err == nil {
			// 更新cfg中的天气
			if sk["date"] != conf.Cfg.Today {
				// 过了一天
				conf.Cfg.Lastday = conf.Cfg.Today
				conf.Cfg.LastdayFc = conf.Cfg.TodayFc
				conf.Cfg.LastdayFd = conf.Cfg.TodayFd
				conf.Cfg.Today = sk["date"]
				conf.Cfg.TodayFc, _ = strconv.Atoi(fc["fc"])
				conf.Cfg.TodayFd, _ = strconv.Atoi(fc["fd"])
				conf.Save()
			} else {
				// 更新计算缓存但不保存
				conf.Cfg.TodayFc, _ = strconv.Atoi(fc["fc"])
				conf.Cfg.TodayFd, _ = strconv.Atoi(fc["fd"])
			}

			msg := "今天是" + sk["date"] + "，" + sk["cityname"] + weather + "，" + fc["fc"] + "到" + fc["fd"] + "度，"
			if conf.Cfg.TodayFc > conf.Cfg.LastdayFc {
				msg += fmt.Sprintf("最高比昨天高%d度，", conf.Cfg.TodayFc-conf.Cfg.LastdayFc)
			} else if conf.Cfg.TodayFc < conf.Cfg.LastdayFc {
				msg += fmt.Sprintf("最高比昨天低%d度，", conf.Cfg.LastdayFc-conf.Cfg.TodayFc)
			}
			if conf.Cfg.TodayFd > conf.Cfg.LastdayFd {
				msg += fmt.Sprintf("最低比昨天高%d度，", conf.Cfg.TodayFd-conf.Cfg.LastdayFd)
			} else if conf.Cfg.TodayFd < conf.Cfg.LastdayFd {
				msg += fmt.Sprintf("最低比昨天低%d度，", conf.Cfg.LastdayFd-conf.Cfg.TodayFd)
			}

			msg += "现在" + sk["weather"] + "，" + sk["temp"] + "度。" + alarm
			// 给android返回当前天气
			fmt.Println("WEATHER " + sk["weather"] + sk["temp"] + "℃")
			// xx区发布雷雨大风红色预警信号 之类的信息
			fmt.Println("WEATHERAL " + alarm)
			return msg
		} else {
			// 错误已经输出过一次了
			return ""
		}
	}
	return ""
}

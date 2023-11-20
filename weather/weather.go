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

func GetWeatherApi(code string) (map[string]string, map[string]string, error) {
	if code != "" {
		resp, err := httpme.Get("http://d1.weather.com.cn/weather_index/"+code+".html", httpme.Header{"Referer": "http://www.weather.com.cn/html/weather/" + code + ".html"})
		if err != nil {
			log.Println("GetWeather 请求出错", err)
			return nil, nil, err
		}
		// 如果code不对会炸无法判断
		str := resp.Text()

		// 感觉没啥用
		// indexStart := strings.Index(str, "weatherinfo\":")
		// indexEnd := strings.Index(str, "};var alarmDZ")
		// jsonCityDZ := str[indexStart+13 : indexEnd]
		// fmt.Println(jsonCityDZ)

		// 名字 当前气温 天气 日期
		indexStart := strings.Index(str, "dataSK =")
		indexEnd := strings.Index(str, ";var dataZS")
		jsonDataSK := str[indexStart+8 : indexEnd]
		log.Println("GetWeather sk", jsonDataSK)

		// 5天预报只取今天
		indexStart = strings.Index(str, "\"f\":[")
		// jsonFC := str[indexStart+4 : len(str)-1]
		indexEnd = strings.Index(str, ",{\"fa")
		jsonFC := str[indexStart+5 : indexEnd]
		log.Println("GetWeather fc", jsonFC)

		var sk, fc map[string]string
		err = json.Unmarshal([]byte(jsonDataSK), &sk)
		if err != nil {
			return sk, fc, err
		}
		err = json.Unmarshal([]byte(jsonFC), &fc)
		if err != nil {
			return sk, fc, err
		}
		return sk, fc, nil
	}
	return nil, nil, errors.New("没有code查什么？")
}

func GetWeather(code string) string {
	if code == "" {
		code = conf.Cfg.WeatherCityCode
	}
	if code != "" {
		sk, fc, err := GetWeatherApi(code)
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

			msg := "今天是" + sk["date"] + "，" + sk["cityname"] + sk["weather"] + "，" + fc["fc"] + "到" + fc["fd"] + "度，"
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
			msg += "现在" + sk["temp"] + "度"
			return msg
		}
	}
	return ""
}

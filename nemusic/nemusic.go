/*
 * 往抑云
 * zyyme 20230630
 * v1.0
 */

package nemusic

import (
	"fmt"
	"log"
	"workdayAlarmClock/conf"

	"github.com/zanjie1999/httpme"
)

// 歌单列表
func PlayList(id string) []string {
	req := httpme.Httpme()
	resp, err := req.Get("http://music.163.com/api/v6/playlist/detail?n=0&id=" + id)
	if err == nil {
		var j map[string]interface{}
		resp.Json(&j)
		if j["code"].(float64) != 200 {
			log.Println("获取歌单信息出错", j["code"], j["message"])
			return []string{}
		}
		tids := j["playlist"].(map[string]interface{})["trackIds"].([]interface{})
		ids := make([]string, len(tids))
		for i, v := range tids {
			ids[i] = fmt.Sprintf("%.0f", v.(map[string]interface{})["id"].(float64))
		}
		return ids
	}
	log.Println("获取歌单信息出错", err)
	return []string{}
}

// 获取音乐播放地址 不一定能放先检查下
func MusicUrl(id string) string {
	req := httpme.Httpme()
	flag := false
	if conf.Cfg.MusicQuality == "" || conf.Cfg.MusicQuality == "standard" {
		// 带这个ua可以放10秒，但没有任何意义
		// resp, err := req.Get("https://music.163.com/song/media/outer/url?id="+id, httpme.Header{"User-Agent": "stagefright/1.2 (Linux;Android 7.0)"})
		resp, err := req.Get("https://music.163.com/song/media/outer/url?id=" + id)
		if err == nil {
			resp.R.Body.Close()
			if resp.R.Request.URL.Path != "/404" {
				// 302后cdn的地址，时间长会过期
				return resp.R.Request.URL.String()
			} else {
				log.Println("需要VIP", id)
				flag = true
			}
		}
		log.Println("检查歌曲是否可用出错", err)
	} else {
		flag = true
	}
	if flag {
		if conf.Cfg.MusicQuality == "" {
			conf.Cfg.MusicQuality = "standard"
		}
		log.Println("获取地址 音质", conf.Cfg.MusicQuality)
		// 使用第三方尝试解析vip  接口谷歌找的
		resp, err := req.Get("https://api.toubiec.cn/wyapi/getMusicUrl.php?level=" + conf.Cfg.MusicQuality + "&id=" + id)
		if err == nil {
			var j map[string]interface{}
			resp.Json(&j)
			if j["code"].(float64) == 200 {
				l := j["data"].([]interface{})
				if len(l) > 0 {
					return l[0].(map[string]interface{})["url"].(string)
				}
			}
		}
		log.Println("使用接口获取歌曲地址出错", err)
	}
	return ""
}

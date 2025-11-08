/*
 * 往抑云
 * zyyme 20230630
 * v1.0
 */

package nemusic

import (
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"workdayAlarmClock/conf"

	"github.com/zanjie1999/httpme"
)

// 歌单列表                歌单id   歌单名  是否缓存
func PlayList(id string) ([]string, string, bool) {
	req := httpme.Httpme()
	resp, err := req.Get("http://music.163.com/api/v6/playlist/detail?n=0&id=" + id)
	if err == nil {
		var j map[string]interface{}
		resp.Json(&j)
		if j["code"].(float64) != 200 {
			log.Println("获取歌单信息出错", j["code"], j["message"])
			return []string{}, j["message"].(string), false
		}
		tids := j["playlist"].(map[string]interface{})["trackIds"].([]interface{})
		ids := make([]string, len(tids))
		for i, v := range tids {
			ids[i] = fmt.Sprintf("%.0f", v.(map[string]interface{})["id"].(float64))
		}
		if conf.Cfg.SavePath != "" {
			// 保存本地缓存
			file, err := os.Create(conf.Cfg.SavePath + id + ".list")
			if err != nil {
				log.Println("创建歌单缓存文件失败", err)
			} else {
				gob.NewEncoder(file).Encode(ids)
			}
			file.Close()
		}
		return ids, j["playlist"].(map[string]interface{})["name"].(string), false
	}
	log.Println("获取歌单信息出错", err)
	if conf.Cfg.SavePath != "" {
		file, err := os.Open(conf.Cfg.SavePath + id + ".list")
		if err == nil {
			log.Println("使用缓存")
			var ids []string
			gob.NewDecoder(file).Decode(&ids)
			return ids, "缓存", true
		} else {
			log.Println("没有缓存", err)
		}
	}
	return []string{}, "无法播放", false
}

// 获取音乐播放地址 不一定能放先检查下
func MusicUrl(id string) string {
	req := httpme.Httpme()
	var url = ""
	// 本地缓存
	if conf.Cfg.SavePath != "" {
		if stat, err := os.Stat(conf.Cfg.SavePath + id + ".mp3"); err == nil && stat.Size() > 0 {
			log.Println("播放缓存", url)
			return conf.Cfg.SavePath + id + ".mp3"
		}
	}
	if conf.Cfg.MusicQuality == "" {
		conf.Cfg.MusicQuality = "standard"
	}
	var err error
	if conf.Cfg.MusicQuality == "standard" {
		// 带这个ua可以放10秒，但没有任何意义
		// resp, err := req.Get("https://music.163.com/song/media/outer/url?id="+id, httpme.Header{"User-Agent": "stagefright/1.2 (Linux;Android 7.0)"})
		var resp *httpme.Response
		resp, err = req.Get("https://music.163.com/song/media/outer/url?id=" + id)
		if err == nil {
			resp.R.Body.Close()
			if resp.R.Request.URL.Path != "/404" {
				// 302后cdn的地址，时间长会过期
				url = resp.R.Request.URL.String()
			} else {
				log.Println("需要VIP", id)
			}
		} else {
			log.Println("检查歌曲是否可用出错", err)
		}
	}
	// 如果err有值则网络异常
	if url == "" && err == nil {
		log.Println("获取地址 音质", conf.Cfg.MusicQuality)
		// 使用第三方尝试解析vip
		resp, err := req.PostJson("https://wyapi.toubiec.cn/api/music/url", "{\"id\":\""+id+"\",\"level\":\""+conf.Cfg.MusicQuality+"\"}", httpme.Header{"sec-fetch-mode": "cros", "referer": "https://wyapi.toubiec.cn/", "origin": "https://wyapi.toubiec.cn"})
		if err == nil {
			var j map[string]interface{}
			resp.Json(&j)
			if j["code"] != nil && j["code"].(float64) == 200 {
				l := j["data"].([]interface{})
				if len(l) > 0 {
					url = l[0].(map[string]interface{})["url"].(string)
				}
			} else {
				log.Println("使用接口获取歌曲地址出错", resp.Text())
			}
		} else {
			log.Println("使用接口获取歌曲地址出错", err)
		}
	}
	if url == "" && err == nil {
		log.Println("获取地址 音质", conf.Cfg.MusicQuality)
		// 使用第三方尝试解析vip
		resp, err := req.Get("https://api.kxzjoker.cn/api/163_music?type=json&ids=" + id + "&level=" + conf.Cfg.MusicQuality)
		if err == nil {
			var j map[string]interface{}
			resp.Json(&j)
			if j["status"] != nil && j["status"].(float64) == 200 {
				url = j["url"].(string)
			} else {
				log.Println("使用接口获取歌曲地址出错", resp.Text())
			}
		} else {
			log.Println("使用接口获取歌曲地址出错", err)
		}
	}
	if conf.Cfg.SavePath != "" && url != "" {
		// 缓存
		log.Println("开始下载到", conf.Cfg.SavePath+id+".mp3")
		resp, err := httpme.Get(url)
		if err != nil {
			log.Println("下载出错", err)
		} else {
			err = resp.SaveFile(conf.Cfg.SavePath + id + ".mp3")
			if err != nil {
				log.Println("保存出错", err)
			} else {
				url = conf.Cfg.SavePath + id + ".mp3"
			}
		}
	}
	return url
}

// 下载歌单缓存
func PlaylistDownload(id string) {
	if conf.Cfg.SavePath == "" {
		log.Println("你还没配置缓存目录")
		return
	}
	ids, name, _ := PlayList(id)
	log.Println("开始下载列表", id, name)
	for i, v := range ids {
		log.Println(len(ids), "/", i+1, v)
		MusicUrl(v)
	}
}

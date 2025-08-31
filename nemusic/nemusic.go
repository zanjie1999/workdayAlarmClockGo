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
		if conf.Cfg.SavePath != "" {
			// 本地缓存
			file, err := os.Create(conf.Cfg.SavePath + id + ".list")
			if err != nil {
				log.Println("创建歌单缓存文件失败", err)
			} else {
				gob.NewEncoder(file).Encode(ids)
			}
			file.Close()
		}
		return ids
	}
	log.Println("获取歌单信息出错", err)
	if conf.Cfg.SavePath != "" {
		file, err := os.Open(conf.Cfg.SavePath + id + ".list")
		if err == nil {
			log.Println("使用缓存")
			var ids []string
			gob.NewDecoder(file).Decode(&ids)
			return ids
		} else {
			log.Println("没有缓存", err)
		}
	}
	return []string{}
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
	if conf.Cfg.MusicQuality == "" || conf.Cfg.MusicQuality == "standard" {
		// 带这个ua可以放10秒，但没有任何意义
		// resp, err := req.Get("https://music.163.com/song/media/outer/url?id="+id, httpme.Header{"User-Agent": "stagefright/1.2 (Linux;Android 7.0)"})
		resp, err := req.Get("https://music.163.com/song/media/outer/url?id=" + id)
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
	if url == "" {
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
					url = l[0].(map[string]interface{})["url"].(string)
				}
			} else {
				log.Println("使用接口获取歌曲地址出错", j)
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
			url = conf.Cfg.SavePath + id + ".mp3"
			resp.SaveFile(url)
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
	log.Println("开始下载列表", id)
	ids := PlayList(id)
	for i, v := range ids {
		log.Println(len(ids), "/", i+1, v)
		MusicUrl(v)
	}
}

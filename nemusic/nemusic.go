package nemusic

import (
	"fmt"
	"log"

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

// 获取音乐播放地址
func MusicUrl(id string) string {
	req := httpme.Httpme()
	resp, err := req.Get("https://music.163.com/song/media/outer/url?id=" + id)
	if err == nil {
		if resp.R.Request.URL.Path != "/404" {
			return resp.R.Request.URL.String()
		} else {
			return ""
		}
	}
	log.Println("检查歌曲是否可用出错", err)
	return ""
}

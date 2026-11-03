/*
 * 音乐来源Api入口
 * zyyme 20230630
 * v2.0
 */

package nemusic

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
)

// 歌单列表                歌单id   歌单名  是否缓存
func PlayList(id string) ([]string, string, bool) {
	// 填的是路径就当本地歌单，不请求接口也不写 .list 缓存，各平台通用
	if isLocalPath(id) {
		return LocalPlayList(id)
	}
	return Current().PlayList(id)
}

// 获取音乐播放地址 不一定能放先检查下
func MusicUrl(id string) string {
	// 本地缓存
	if conf.Cfg.SavePath != "" {
		cacheFile := conf.Cfg.SavePath + CacheBase(id) + ".mp3"
		if stat, err := os.Stat(cacheFile); err == nil && stat.Size() > 0 {
			log.Println("播放缓存", cacheFile)
			return cacheFile
		}
	}
	if conf.Cfg.MusicQuality == "" {
		conf.Cfg.MusicQuality = "standard"
	}
	log.Println("获取地址 音质", conf.Cfg.MusicQuality)
	url, err := Current().MusicURL(id, conf.Cfg.MusicQuality)
	if err != nil {
		log.Println("获取歌曲地址出错", err)
		return ""
	}
	if url == "" {
		log.Println("获取歌曲地址失败")
		return ""
	}
	log.Println("获取歌曲地址成功")
	if conf.Cfg.SavePath == "" {
		return url
	}
	removed, err := removeOldestMp3IfFull(conf.Cfg.SavePath, conf.Cfg.CacheLimit)
	if err != nil {
		log.Println("清理缓存失败，跳过缓存下载", err)
		return url
	}
	if removed != "" {
		log.Println("缓存达到上限，删除最旧文件", removed)
	}
	// 下载用时较长，先暂停
	if conf.IsApp {
		app.SendLocal("PAUSE")
	}
	cacheFile := conf.Cfg.SavePath + CacheBase(id) + ".mp3"
	log.Println("开始下载到", cacheFile)
	if err := downloadTo(url, cacheFile); err != nil {
		log.Println("下载出错", err)
		return url
	}
	return cacheFile
}

func removeOldestMp3IfFull(savePath string, limitMB int64) (string, error) {
	const bytesPerMB int64 = 1024 * 1024
	if limitMB <= 0 || limitMB > (1<<63-1)/bytesPerMB {
		return "", fmt.Errorf("缓存上限无效: %d MB", limitMB)
	}

	entries, err := os.ReadDir(savePath)
	if err != nil {
		return "", fmt.Errorf("读取缓存目录失败: %w", err)
	}

	var totalSize int64
	var oldestPath string
	var oldestTime time.Time
	for _, entry := range entries {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".mp3") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", fmt.Errorf("读取缓存文件信息失败 %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		totalSize += info.Size()
		if oldestPath == "" || info.ModTime().Before(oldestTime) {
			oldestPath = filepath.Join(savePath, entry.Name())
			oldestTime = info.ModTime()
		}
	}

	if totalSize < limitMB*bytesPerMB {
		return "", nil
	}
	if oldestPath == "" {
		return "", fmt.Errorf("缓存已达到上限，但没有可清理的MP3文件")
	}
	if err := os.Remove(oldestPath); err != nil {
		return "", fmt.Errorf("删除最旧缓存文件失败 %s: %w", oldestPath, err)
	}
	return oldestPath, nil
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

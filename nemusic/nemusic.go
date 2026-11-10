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
	"sync"
	"time"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
)

// 歌单列表
func PlayList(id string) ([]string, []string, string, bool) {
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
	ids, _, name, _ := PlayList(id)
	log.Println("开始下载列表", id, name)
	for i, v := range ids {
		log.Println(len(ids), "/", i+1, v)
		MusicUrl(v)
	}
}

// ---------------------------------------------------------------- 曲名

// songNameCache 曲名缓存：一次会话里同一首只查一次（查成功才缓存，失败下次还能重试）。
// 循环播放同一首时不会重复发请求。
var songNameCache sync.Map // id -> string

// SongName 取当前来源的曲名，供外壳的 ECHO 显示。
// 来源不支持 / 查询失败 / 本地路径 / 直链一律返回空串，调用方据此退回老文案
// （"待播放 N" / "正在播放"），不能因为查不到名字影响播放。
func SongName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || isLocalPath(id) || strings.HasPrefix(id, "http") {
		return ""
	}
	if v, ok := songNameCache.Load(id); ok {
		return v.(string)
	}
	namer, ok := Current().(SongNamer)
	if !ok {
		return "" // 这个平台没实现
	}
	name, err := namer.SongName(id)
	if err != nil {
		log.Println("获取歌曲名出错", id, err)
		return ""
	}
	if name != "" {
		songNameCache.Store(id, name)
	}
	return name
}

func trimLabel(s string, maxRunes int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes])
}

/*
 * 音乐来源注册表
 * 每个平台实现 Source，按配置切换，取歌单和取播放地址都从这里分发
 * zyyme 20261103
 */

package nemusic

import (
	"encoding/gob"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"workdayAlarmClock/conf"
)

// Source 一个音乐平台需要提供的最小能力。
// 只负责取歌单内的歌曲 id 和单曲播放地址，缓存与下载统一在 nemusic.go 里做。
type Source interface {
	// PlayList 返回歌单内的歌曲 id 列表、歌单名、是否来自本地缓存
	PlayList(id string) ([]string, string, bool)
	// MusicURL 按音质返回可直接播放的地址
	MusicURL(id, quality string) (string, error)
	// CachePrefix 缓存文件名前缀，避免不同平台的 id 撞名
	CachePrefix() string
	// ParsePlaylistID / ParseSongID 把分享链接或裸 id 归一化成平台 id
	ParsePlaylistID(raw string) string
	ParseSongID(raw string) string
}

// sources 注册表，新增平台在这里加一行即可
var sources = map[string]Source{
	conf.MusicApiNetease: neteaseSource{},
	conf.MusicApiQQ:      qqSource{},
	conf.MusicApiKugou:   kugouSource{},
}

// cookieValue 从 cookie 串里取指定 key 的值，keys 越靠前优先级越高
func cookieValue(cookie string, keys ...string) string {
	parts := strings.Split(cookie, ";")
	for _, want := range keys {
		for _, part := range parts {
			part = strings.TrimSpace(part)
			key, value, ok := strings.Cut(part, "=")
			if ok && key == want {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// digitsOnly 只保留数字字符，其它字符多说明不是 id
func digitsOnly(s string) string {
	if s == "" {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// flexInt64 有些接口的 id 一会儿是数字一会儿是字符串，统一按数字收
type flexInt64 int64

func (v *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*v = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// 解析不了就当没有，不影响其它字段
		*v = 0
		return nil
	}
	*v = flexInt64(n)
	return nil
}

// Current 当前配置的音乐来源，未知值回落网易云
func Current() Source {
	if s, ok := sources[conf.Cfg.MusicApi]; ok {
		return s
	}
	return sources[conf.MusicApiNetease]
}

// ParseID 按当前来源解析歌单/歌曲 id，本地路径原样返回
// kind: playlist | song
func ParseID(kind, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || isLocalPath(raw) {
		return raw
	}
	if kind == "song" {
		return Current().ParseSongID(raw)
	}
	return Current().ParsePlaylistID(raw)
}

// cacheNameRe 只保留文件名安全的字符，避免 id 里的特殊字符写坏路径
var cacheNameRe = regexp.MustCompile(`[^0-9a-zA-Z_-]`)

// CacheBase 缓存文件主名，带平台前缀，写缓存和判断缓存是否命中都用它
func CacheBase(id string) string {
	return Current().CachePrefix() + cacheNameRe.ReplaceAllString(id, "_")
}

// listCachePath 歌单 id 列表的缓存路径
func listCachePath(id string) string {
	return conf.Cfg.SavePath + CacheBase(id) + ".list"
}

// saveListCache 保存歌单 id 列表缓存，没配缓存目录就不存
func saveListCache(id string, ids []string) {
	if conf.Cfg.SavePath == "" {
		return
	}
	file, err := os.Create(listCachePath(id))
	if err != nil {
		log.Println("创建歌单缓存文件失败", err)
		return
	}
	defer file.Close()
	if err := gob.NewEncoder(file).Encode(ids); err != nil {
		log.Println("写入歌单缓存失败", err)
	}
}

// loadListCache 读取歌单 id 列表缓存
func loadListCache(id string) ([]string, bool) {
	if conf.Cfg.SavePath == "" {
		return nil, false
	}
	file, err := os.Open(listCachePath(id))
	if err != nil {
		log.Println("没有歌单缓存", err)
		return nil, false
	}
	defer file.Close()
	var ids []string
	if err := gob.NewDecoder(file).Decode(&ids); err != nil {
		log.Println("读取歌单缓存失败", err)
		return nil, false
	}
	log.Println("使用歌单缓存")
	return ids, true
}

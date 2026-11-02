/*
 * 本地目录歌单
 * 歌单配置填 ./ 或 / 开头的路径时，直接把该目录当成歌单
 * zyyme 20261103
 */

package nemusic

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 防止有人把根目录当歌单，列表撑爆内存
const localPlayListLimit = 20000

// isLocalPath 判断歌单配置值是否要按本地路径处理
func isLocalPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "./"), strings.HasPrefix(p, "../"),
		strings.HasPrefix(p, ".\\"), strings.HasPrefix(p, "..\\"),
		strings.HasPrefix(p, "/"):
		return true
	}
	// Windows 盘符，C:/xxx 或 C:\xxx
	return len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// LocalPlayList 把本地路径展开成播放列表
// 返回的路径保留传入时的 ./ 或 / 或盘符前缀，player.Next 靠这个前缀判断直连播放
func LocalPlayList(p string) ([]string, string, bool) {
	info, err := os.Stat(p)
	if err != nil {
		log.Println("本地歌单路径打不开", p, err)
		return []string{}, "路径不存在", false
	}
	if !info.IsDir() {
		return []string{p}, filepath.Base(p), false
	}
	prefix := strings.TrimRight(filepath.ToSlash(p), "/")
	var list []string
	err = filepath.WalkDir(p, func(target string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Println("扫描本地歌单出错", target, err)
			return nil
		}
		if d.IsDir() {
			// 跳过目录本身以外以点开头的目录，顺带避开 webdav 的临时文件
			if target != p && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(p, target)
		if err != nil {
			return nil
		}
		list = append(list, prefix+"/"+filepath.ToSlash(rel))
		if len(list) >= localPlayListLimit {
			log.Println("本地歌单文件太多，只取前", localPlayListLimit, "个")
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		log.Println("扫描本地歌单失败", p, err)
	}
	sort.Strings(list)
	log.Println("本地歌单", p, "共", len(list), "个文件")
	return list, filepath.Base(prefix), false
}

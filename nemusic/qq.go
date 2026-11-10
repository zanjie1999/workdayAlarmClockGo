/*
 * QQ音乐 歌单与播放地址解析
 * 只做「取歌单 id 列表」和「取单曲播放地址」两件事，其余交给前端直连代理
 * zyyme 20261103
 */

package nemusic

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"workdayAlarmClock/conf"

	"github.com/zanjie1999/httpme"
)

const (
	// 歌单详情
	qqPlaylistAPI = "https://c.y.qq.com/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg"
	// 播放地址 vkey 接口
	qqMusicuAPI = "https://u.y.qq.com/cgi-bin/musicu.fcg"
	qqReferer   = "https://y.qq.com/"
	// vkey 接口要求带 guid，固定值即可
	qqGUID = "10000"
)

// qqSource QQ音乐实现
type qqSource struct{}

// CachePrefix QQ音乐的缓存文件前缀，避免和网易云的数字 id 撞名
func (qqSource) CachePrefix() string { return "qq_" }

// qqQuality 音质档位对应的文件名前缀和扩展名，接口按后缀返回对应容器
func qqQuality(quality string) (prefix, ext string) {
	switch quality {
	case "exhigh":
		return "M800", ".mp3"
	case "lossless", "hires":
		return "F000", ".flac"
	default:
		return "M500", ".mp3"
	}
}

func (qqSource) MusicURL(id, quality string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("歌曲id为空")
	}
	return qqSongURL(id, quality, QQCookieUin(conf.Cfg.Cookie))
}

type qqComm struct {
	Uin    string `json:"uin"`
	Format string `json:"format"`
	Ct     int    `json:"ct"`
	Cv     int    `json:"cv"`
}

type qqVkeyParam struct {
	GUID      string   `json:"guid"`
	SongMid   []string `json:"songmid"`
	SongType  []int    `json:"songtype"`
	Uin       string   `json:"uin"`
	LoginFlag int      `json:"loginflag"`
	Platform  string   `json:"platform"`
	Filename  []string `json:"filename"`
}

type qqVkeyReq struct {
	Module string      `json:"module"`
	Method string      `json:"method"`
	Param  qqVkeyParam `json:"param"`
}

type qqMusicuRequest struct {
	Comm qqComm    `json:"comm"`
	Req0 qqVkeyReq `json:"req_0"`
}

type qqMusicuResponse struct {
	Req0 struct {
		Code int `json:"code"`
		Data struct {
			Sip        []string `json:"sip"`
			Midurlinfo []struct {
				Purl     string `json:"purl"`
				Filename string `json:"filename"`
			} `json:"midurlinfo"`
		} `json:"data"`
	} `json:"req_0"`
}

// qqSongURL 请求 vkey 接口拿播放地址
func qqSongURL(mid, quality, uin string) (string, error) {
	if uin == "" {
		uin = "0"
	}
	prefix, ext := qqQuality(quality)
	body := qqMusicuRequest{
		Comm: qqComm{Uin: uin, Format: "json", Ct: 24, Cv: 0},
		Req0: qqVkeyReq{
			Module: "vkey.GetVkeyServer",
			Method: "CgiGetVkey",
			Param: qqVkeyParam{
				GUID:      qqGUID,
				SongMid:   []string{mid},
				SongType:  []int{0},
				Uin:       uin,
				LoginFlag: 1,
				Platform:  "20",
				Filename:  []string{prefix + mid + mid + ext},
			},
		},
	}
	headers := httpme.Header{
		"Referer": qqReferer,
		"Cookie":  conf.Cfg.Cookie,
	}
	resp, err := httpme.Httpme().PostJson(qqMusicuAPI, headers, body)
	if err != nil {
		return "", fmt.Errorf("请求接口出错: %w", err)
	}
	var j qqMusicuResponse
	if err := resp.Json(&j); err != nil {
		return "", fmt.Errorf("接口返回解析失败: %w", err)
	}
	if j.Req0.Code != 0 {
		return "", fmt.Errorf("接口返回异常: %d", j.Req0.Code)
	}
	if len(j.Req0.Data.Midurlinfo) == 0 {
		return "", fmt.Errorf("接口没有返回歌曲信息")
	}
	item := j.Req0.Data.Midurlinfo[0]
	if item.Purl == "" {
		return "", fmt.Errorf("没有拿到播放地址，可能需要VIP或没有版权")
	}
	sip := ""
	if len(j.Req0.Data.Sip) > 0 {
		sip = j.Req0.Data.Sip[0]
	}
	return sip + item.Purl, nil
}

func (qqSource) PlayList(id string) ([]string, []string, string, bool) {
	ids, names, name, err := qqPlayListIDs(id)
	if err == nil && len(ids) > 0 {
		saveListCache(id, ids)
		return ids, names, name, false
	}
	if err != nil {
		log.Println("QQ音乐获取歌单出错", err)
	}
	if cached, ok := loadListCache(id); ok {
		return cached, nil, "缓存", true
	}
	if name != "" {
		return []string{}, nil, name, false
	}
	return []string{}, nil, "无法播放", false
}

type qqCDInfo struct {
	Code   int `json:"code"`
	CDList []struct {
		DissName string `json:"dissname"`
		SongNum  int    `json:"songnum"`
		SongList []struct {
			SongMid  string `json:"songmid"`
			SongName string `json:"songname"`
		} `json:"songlist"`
	} `json:"cdlist"`
}

// qqPlayListIDs 歌单详情，流式解码取歌曲 mid 和歌名
func qqPlayListIDs(id string) ([]string, []string, string, error) {
	target := qqPlaylistAPI + "?type=1&json=1&utf8=1&onlysong=0&format=json&disstid=" + url.QueryEscape(id) +
		"&g_tk=5381&loginUin=0&hostUin=0&inCharset=utf8&outCharset=utf-8&notice=0&platform=yqq.json&needNewCode=0"
	resp, err := httpme.Httpme().GetStream(target, httpme.Header{
		"Referer": qqReferer,
		"Cookie":  conf.Cfg.Cookie,
	})
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.R.Body.Close()
	if resp.R.StatusCode != http.StatusOK {
		return nil, nil, "", fmt.Errorf("接口状态异常: %s", resp.R.Status)
	}
	var j qqCDInfo
	if err := json.NewDecoder(resp.R.Body).Decode(&j); err != nil {
		return nil, nil, "", fmt.Errorf("解析歌单失败: %w", err)
	}
	if len(j.CDList) == 0 {
		return nil, nil, "", fmt.Errorf("歌单不存在或没有权限 code=%d", j.Code)
	}
	cd := j.CDList[0]
	ids := make([]string, 0, len(cd.SongList))
	names := make([]string, 0, len(cd.SongList))
	for _, s := range cd.SongList {
		if s.SongMid == "" {
			continue
		}
		ids = append(ids, s.SongMid)
		names = append(names, s.SongName)
	}
	return ids, names, cd.DissName, nil
}

// QQCookieUin 从 cookie 里取QQ号，取不到返回空串
func QQCookieUin(cookie string) string {
	raw := cookieValue(cookie, "uin", "qqmusic_uin", "p_uin", "ptui_loginuin", "luin")
	// 网页版 cookie 里 uin 带个 o 前缀并且左补0，比如 o0123456789 其实是 123456789
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "o")
	raw = digitsOnly(raw)
	raw = strings.TrimLeft(raw, "0")
	return raw
}

var (
	// 网页版是 ryqq 或 ryqq_v2，手机分享页是 taoge.html?...&id=，两种都认
	qqPlaylistRe = regexp.MustCompile(`(?:playlist/|taoge\.html[^#]*?[?&]id=|disstid=)(\d+)`)
	qqSongRe     = regexp.MustCompile(`(?:songDetail/|songmid=)([0-9A-Za-z]{8,20})`)
	qqMidRe      = regexp.MustCompile(`^[0-9A-Za-z]{8,20}$`)
	qqShortRe    = regexp.MustCompile(`(https?://)?c6\.y\.qq\.com/[^\s"'<>，。；、)）]+`)
)

func (qqSource) ParsePlaylistID(raw string) string {
	if m := qqShortRe.FindStringSubmatch(raw); m != nil {
		if url := followShortLink(m[0]); url != "" {
			raw = url
		}
	}
	if m := qqPlaylistRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}

func (qqSource) ParseSongID(raw string) string {
	if m := qqShortRe.FindStringSubmatch(raw); m != nil {
		if url := followShortLink(m[0]); url != "" {
			raw = url
		}
	}
	if m := qqSongRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if qqMidRe.MatchString(raw) {
		return raw
	}
	return raw
}

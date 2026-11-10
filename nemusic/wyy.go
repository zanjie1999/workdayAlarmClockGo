/*
 * 网易云音乐 weapi 播放地址解析
 * weapi 的 params 需要 AES 两次加密，encSecKey 用 RSA 加密随机密钥
 * zyyme 20261103
 */
package nemusic

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"workdayAlarmClock/conf"

	"github.com/zanjie1999/httpme"
)

const (
	// 第一次 AES 的固定密钥和固定 iv
	wyyAESKey = "0CoJUm6Qyw8W8jud"
	wyyAESIV  = "0102030405060708"
	// RSA 的公钥指数和模数
	wyyRSAExp = "10001"
	wyyRSAMod = "00e0b509f6259df8642dbc35662901477df22677ec152b5ff68ace615bb7b725152b3ab17a876aea8a5aa76d2e417629ec4ee341f56135fccf695280104e0312ecbda92557c93870114af6c9d05c4f7f0c3685b7a46bee255932575cce10b424d813cfe4875d3e82047b97ddef52741d546b8e289dc6935b3ece0462db0a22b8e7"
	// 随机密钥的字符表
	wyyKeyChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// 获取播放地址的接口
	wyySongApi = "https://music.163.com/weapi/song/enhance/player/url/v1"
)

// RSA 模数只解析一次
var wyyRSAModulus = func() *big.Int {
	n, ok := new(big.Int).SetString(wyyRSAMod, 16)
	if !ok {
		panic("网易云RSA模数解析失败")
	}
	return n
}()

type wyySongRequest struct {
	IDs        string `json:"ids"`
	CSRFToken  string `json:"csrf_token"`
	Level      string `json:"level"`
	EncodeType string `json:"encodeType"`
}

type wyySongItem struct {
	ID         int64   `json:"id"`
	URL        *string `json:"url"`
	Code       int     `json:"code"`
	Level      string  `json:"level"`
	EncodeType string  `json:"encodeType"`
	Type       string  `json:"type"`
	Message    *string `json:"message"`
}

type wyySongResponse struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    []wyySongItem `json:"data"`
}

// 请求网易云接口获取播放地址，失败返回错误
func wyySongURL(req *httpme.Request, id string, level string) (string, error) {
	csrf := csrfFromCookie(conf.Cfg.Cookie)
	body, err := json.Marshal(wyySongRequest{
		IDs:        "[" + id + "]",
		CSRFToken:  csrf,
		Level:      level,
		EncodeType: encodeTypeByLevel(level),
	})
	if err != nil {
		return "", fmt.Errorf("构造请求参数失败: %w", err)
	}
	params, encSecKey, err := weapiSign(string(body))
	if err != nil {
		return "", fmt.Errorf("加密请求参数失败: %w", err)
	}
	headers := httpme.Header{
		"Referer": "https://music.163.com/",
		"Cookie":  conf.Cfg.Cookie,
	}
	resp, err := req.Post(wyySongApi+"?csrf_token="+csrf, httpme.Datas{
		"params":    params,
		"encSecKey": encSecKey,
	}, headers)
	if err != nil {
		return "", fmt.Errorf("请求接口出错: %w", err)
	}
	var j wyySongResponse
	if err := resp.Json(&j); err != nil {
		// 出错时接口可能返回多段json，把原始内容带出来方便排查
		text := resp.Text()
		if len(text) > 200 {
			text = text[:200]
		}
		return "", fmt.Errorf("接口返回解析失败: %v, 内容: %s", err, text)
	}
	if j.Code != 200 {
		return "", fmt.Errorf("接口返回异常: %d %s", j.Code, j.Message)
	}
	if len(j.Data) == 0 {
		return "", fmt.Errorf("接口没有返回歌曲信息")
	}
	item := j.Data[0]
	if item.Code != 200 {
		msg := ""
		if item.Message != nil {
			msg = *item.Message
		}
		return "", fmt.Errorf("歌曲不可播放: %d %s", item.Code, msg)
	}
	if item.URL == nil || *item.URL == "" {
		return "", fmt.Errorf("没有拿到播放地址，可能需要VIP或没有版权")
	}
	return *item.URL, nil
}

// 接口必须带 encodeType，不带会返回400参数错误
// 播放链路只支持mp3，所以无损才用flac
func encodeTypeByLevel(level string) string {
	switch level {
	case "lossless", "hires":
		return "flac"
	default:
		return "mp3"
	}
}

// weapi 签名，返回 params 和 encSecKey
func weapiSign(text string) (string, string, error) {
	key, err := randKey(16)
	if err != nil {
		return "", "", err
	}
	first, err := aesEncryptCBC([]byte(text), wyyAESKey, wyyAESIV)
	if err != nil {
		return "", "", err
	}
	params, err := aesEncryptCBC([]byte(first), key, wyyAESIV)
	if err != nil {
		return "", "", err
	}
	encSecKey, err := rsaEncrypt(reverseString(key))
	if err != nil {
		return "", "", err
	}
	return params, encSecKey, nil
}

// AES-CBC 加密，PKCS7 补齐后返回标准 base64（不带换行）
func aesEncryptCBC(plainText []byte, key string, iv string) (string, error) {
	block, err := aes.NewCipher(to16Bytes(key))
	if err != nil {
		return "", err
	}
	data := pkcs7Pad(plainText, block.BlockSize())
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, to16Bytes(iv)).CryptBlocks(out, data)
	return base64.StdEncoding.EncodeToString(out), nil
}

// RSA 加密，返回256位hex（左侧补0）
func rsaEncrypt(text string) (string, error) {
	e, ok := new(big.Int).SetString(wyyRSAExp, 16)
	if !ok {
		return "", fmt.Errorf("RSA指数解析失败")
	}
	m := new(big.Int).SetBytes([]byte(text))
	c := new(big.Int).Exp(m, e, wyyRSAModulus)
	return fmt.Sprintf("%0256x", c), nil
}

// 生成指定长度的随机字符串
func randKey(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = wyyKeyChars[int(v)%len(wyyKeyChars)]
	}
	return string(out), nil
}

// 从cookie里取 __csrf，没有返回空字符串
func csrfFromCookie(cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "__csrf="); ok {
			return v
		}
	}
	return ""
}

// 密钥不足16字节时补0，超过则截断
func to16Bytes(key string) []byte {
	b := []byte(key)
	for len(b)%aes.BlockSize != 0 {
		b = append(b, 0)
	}
	return b
}

// PKCS7 补齐，整除块大小时补满一整块
func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
}

// 反转字符串，用于生成 RSA 明文
func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// neteaseSource 网易云音乐实现
type neteaseSource struct{}

// CachePrefix 网易云沿用原来的 <id>.mp3，不带前缀
func (neteaseSource) CachePrefix() string { return "" }

func (neteaseSource) MusicURL(id, quality string) (string, error) {
	return wyySongURL(httpme.Httpme(), id, quality)
}

func (neteaseSource) PlayList(id string) ([]string, []string, string, bool) {
	ids, name, err := wyyPlayListIDs(id)
	if err == nil {
		saveListCache(id, ids)
		// 网易云歌单接口不带歌名，这里 names 填空，歌名走 SongNamer 单独查
		return ids, nil, name, false
	}
	log.Println("获取歌单信息出错", err)
	// 接口不通时放缓存，断网闹钟也能响
	if cached, ok := loadListCache(id); ok {
		return cached, nil, "缓存", true
	}
	return []string{}, nil, "无法播放", false
}

// SongName 查歌名。查不到返回空串而不是错误，调用方据此退回老文案
func (neteaseSource) SongName(id string) (string, error) {
	req := httpme.Httpme()
	// 只等 3 秒：这个名字用来刷屏，卡住会拖慢切歌
	req.SetTimeout(3)
	defer req.Close()

	resp, err := req.Get("http://music.163.com/api/song/detail?ids=" + url.QueryEscape("["+id+"]"))
	if err != nil {
		return "", fmt.Errorf("请求歌曲详情出错: %w", err)
	}
	var j wyySongDetail
	if err := resp.Json(&j); err != nil {
		return "", fmt.Errorf("解析歌曲详情失败: %w", err)
	}
	if j.Code != 200 {
		return "", fmt.Errorf("接口返回异常: %d", j.Code)
	}
	if len(j.Songs) == 0 {
		return "", fmt.Errorf("接口没有返回歌曲信息")
	}
	return j.Songs[0].Name, nil
}

// song/detail 的歌名在 songs[0].name，
// 歌手字段有两个版本：老接口叫 artists，v3 叫 ar，都收着
type wyySongDetail struct {
	Code  int `json:"code"`
	Songs []struct {
		Name    string `json:"name"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		Ar []struct {
			Name string `json:"name"`
		} `json:"ar"`
	} `json:"songs"`
}

// wyyPlayListIDs 歌单详情接口
func wyyPlayListIDs(id string) ([]string, string, error) {
	// 带个Cookie可以直接放私人雷达
	resp, err := httpme.Httpme().GetStream("http://music.163.com/api/v6/playlist/detail?n=0&id="+id, httpme.Header{"Cookie": conf.Cfg.Cookie})
	if err != nil {
		return nil, "", err
	}
	defer resp.R.Body.Close()
	if resp.R.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("接口状态异常: %s", resp.R.Status)
	}
	var j wyyPlayListDetail
	if err := json.NewDecoder(resp.R.Body).Decode(&j); err != nil {
		return nil, "", fmt.Errorf("解析歌单失败: %w", err)
	}
	if j.Code != 200 {
		return nil, "", fmt.Errorf("接口返回异常: %d %s", j.Code, j.Message)
	}
	ids := make([]string, len(j.Playlist.TrackIDs))
	for i, v := range j.Playlist.TrackIDs {
		ids[i] = strconv.FormatInt(v.ID, 10)
	}
	return ids, j.Playlist.Name, nil
}

type wyyPlayListDetail struct {
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Playlist struct {
		Name     string `json:"name"`
		TrackIDs []struct {
			ID int64 `json:"id"`
		} `json:"trackIds"`
	} `json:"playlist"`
}

// 链接解析用的正则，包级只编译一次
var (
	neteaseNumericRe   = regexp.MustCompile(`^\d+$`)
	neteaseShortLinkRe = regexp.MustCompile(`(https?://)?163cn\.tv/[^\s"'<>，。；、)）]+`)
	neteasePlaylistRe  = regexp.MustCompile(`playlist\?[^#]*?id=(\d+)`)
	neteaseSongRe      = regexp.MustCompile(`song\?[^#]*?id=(\d+)`)
)

func (neteaseSource) ParsePlaylistID(raw string) string {
	return parseNeteaseID(raw, neteasePlaylistRe)
}

func (neteaseSource) ParseSongID(raw string) string { return parseNeteaseID(raw, neteaseSongRe) }

// parseNeteaseID 裸数字直接用，163cn.tv 短链跟随跳转，其它链接里抠 id=
func parseNeteaseID(s string, re *regexp.Regexp) string {
	if neteaseNumericRe.MatchString(s) {
		return s
	}
	if m := neteaseShortLinkRe.FindStringSubmatch(s); m != nil {
		if url := followShortLink(m[0]); url != "" {
			s = url
		}
	}
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// followShortLink 跟随短链跳转，返回最终地址，失败返回空
func followShortLink(url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	resp, err := httpme.Httpme().GetStream(url)
	if err != nil {
		log.Println("解析短链失败", err)
		return ""
	}
	resp.R.Body.Close()
	if resp.R.Request == nil || resp.R.Request.URL == nil {
		return ""
	}
	return resp.R.Request.URL.String()
}

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
	"math/big"
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
	}
	if conf.Cfg.Cookie != "" {
		headers["Cookie"] = conf.Cfg.Cookie
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

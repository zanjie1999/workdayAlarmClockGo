/*
 * 往抑云
 * zyyme 20230630
 * v1.0
 */

package nemusic

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"

	"github.com/zanjie1999/httpme"
)

type nextMusicKeyData struct {
	KeyID    string `json:"keyId"`
	KeyToken string `json:"keyToken"`
	Key      string `json:"key"`
}

type nextMusicKeyResponse struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    nextMusicKeyData `json:"data"`
}

type nextMusicSongData struct {
	URL string `json:"url"`
}

type nextMusicSongResponse struct {
	Code       int               `json:"code"`
	Message    string            `json:"message"`
	Ciphertext string            `json:"ciphertext"`
	Data       nextMusicSongData `json:"data"`
}

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
		url, err = nextMusicSongURL(req, id, conf.Cfg.MusicQuality)
		if err == nil {
			if url == "" {
				log.Println("使用接口获取歌曲地址解析出错", "接口未返回歌曲地址")
			} else {
				log.Println("使用接口获取歌曲地址成功", url)
			}
		} else {
			// 因为有时候会失败 第二次又好了
			if strings.Contains(err.Error(), "Song not found") {
				log.Println("重试一次")
				time.Sleep(time.Second)
				url, err = nextMusicSongURL(req, id, conf.Cfg.MusicQuality)
			}
			if err != nil {
				log.Println("使用接口获取歌曲地址出错", err)
			}
		}
	}
	if conf.Cfg.SavePath != "" && url != "" {
		// 下载用时较长，先暂停
		if conf.IsApp {
			app.SendLocal("PAUSE")
		}
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

func nextMusicHeaders() httpme.Header {
	return httpme.Header{
		"sec-fetch-mode": "cros",
		"referer":        "https://wyapi.toubiec.cn/",
		"origin":         "https://wyapi.toubiec.cn",
	}
}

func nextMusicKey(req *httpme.Request) (nextMusicKeyData, error) {
	resp, err := req.PostJson("https://nextmusic.toubiec.cn/api/key", nextMusicHeaders())
	if err != nil {
		return nextMusicKeyData{}, fmt.Errorf("key请求失败: %w", err)
	}

	var j nextMusicKeyResponse
	if err := resp.Json(&j); err != nil {
		return nextMusicKeyData{}, fmt.Errorf("key响应解析失败: %w", err)
	}
	if j.Code != 200 {
		if j.Message == "" {
			j.Message = resp.Text()
		}
		return nextMusicKeyData{}, fmt.Errorf("key接口返回异常: %s", j.Message)
	}
	if j.Data.KeyID == "" || j.Data.KeyToken == "" || j.Data.Key == "" {
		return nextMusicKeyData{}, fmt.Errorf("key接口返回数据不完整")
	}
	return j.Data, nil
}

func nextMusicAEAD(keyText string) (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil {
		return nil, fmt.Errorf("key base64解码失败: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES初始化失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM初始化失败: %w", err)
	}
	return aead, nil
}

func encryptNextMusicPayload(payload interface{}, keyText string) (string, error) {
	aead, err := nextMusicAEAD(keyText)
	if err != nil {
		return "", err
	}
	plainText, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("请求数据编码失败: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("随机数生成失败: %w", err)
	}

	sealed := aead.Seal(nil, nonce, plainText, nil)
	tagSize := aead.Overhead()
	cipherText := sealed[:len(sealed)-tagSize]
	tag := sealed[len(sealed)-tagSize:]
	return strings.Join([]string{
		base64.StdEncoding.EncodeToString(nonce),
		base64.StdEncoding.EncodeToString(tag),
		base64.StdEncoding.EncodeToString(cipherText),
	}, "."), nil
}

func decryptNextMusicCiphertext(ciphertext string, keyText string) ([]byte, error) {
	aead, err := nextMusicAEAD(keyText)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(ciphertext, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("响应密文格式错误")
	}
	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("响应nonce解码失败: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("响应tag解码失败: %w", err)
	}
	cipherText, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("响应密文解码失败: %w", err)
	}

	sealed := make([]byte, 0, len(cipherText)+len(tag))
	sealed = append(sealed, cipherText...)
	sealed = append(sealed, tag...)
	plainText, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("响应解密失败: %w", err)
	}
	return plainText, nil
}

func nextMusicSongURL(req *httpme.Request, id string, level string) (string, error) {
	keyData, err := nextMusicKey(req)
	if err != nil {
		return "", err
	}

	data, err := encryptNextMusicPayload(map[string]interface{}{
		"id":        id,
		"level":     level,
		"timestamp": time.Now().UnixMilli(),
	}, keyData.Key)
	if err != nil {
		return "", fmt.Errorf("getSongUrl请求加密失败: %w", err)
	}

	resp, err := req.PostJson("https://nextmusic.toubiec.cn/api/getSongUrl", map[string]string{
		"keyId":    keyData.KeyID,
		"keyToken": keyData.KeyToken,
		"data":     data,
	}, nextMusicHeaders())
	if err != nil {
		return "", fmt.Errorf("getSongUrl请求失败: %w", err)
	}

	var j nextMusicSongResponse
	if err := resp.Json(&j); err != nil {
		return "", fmt.Errorf("getSongUrl响应解析失败: %w", err)
	}
	if j.Ciphertext != "" {
		plainText, err := decryptNextMusicCiphertext(j.Ciphertext, keyData.Key)
		if err != nil {
			return "", fmt.Errorf("getSongUrl响应解密失败: %w", err)
		}
		if err := json.Unmarshal(plainText, &j); err != nil {
			return "", fmt.Errorf("getSongUrl解密响应解析失败: %w", err)
		}
	}
	if j.Code != 200 {
		if j.Message == "" {
			j.Message = resp.Text()
		}
		return "", fmt.Errorf("getSongUrl接口返回异常: %s", j.Message)
	}
	if j.Data.URL == "" {
		return "", fmt.Errorf("getSongUrl接口未返回歌曲地址")
	}
	return j.Data.URL, nil
}

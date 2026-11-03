/*
 * http服务路由 文档https://gin-gonic.com/zh-cn/docs/
 * zyyme 202305023
 * v1.0
 */

package router

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"workdayAlarmClock/app"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/display"
	"workdayAlarmClock/nemusic"
	"workdayAlarmClock/player"
	"workdayAlarmClock/update"
	"workdayAlarmClock/weather"

	"github.com/gin-gonic/gin"
)

// 下面这个注释配置了需要打包进二进制文件的静态文件
//
//go:embed static/*
var f embed.FS

var (
	js2home = "\n<script>setInterval(function(){window.history.go(-1)},3000);</script><style>@media(prefers-color-scheme:dark){body{color:#e0e0e0;background-color:#121212;}</style>"
	js2back = "<script>window.history.go(-1)</script>"
)

// 第三方接口代理用的客户端，连接复用，弱设备少握手
var apiClient = &http.Client{Timeout: 30 * time.Second}

// proxyStream 把上游响应原样流式写回，不解析也不缓存，避免占内存
func proxyStream(c *gin.Context, method, target, body string, header map[string]string) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), method, target, reader)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	for key, value := range header {
		if value != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	// 只回传 Content-Type，上游的 Set-Cookie 之类不能带给浏览器
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Writer.Header().Set("Content-Type", ct)
	}
	c.Status(resp.StatusCode)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	_, _ = io.Copy(c.Writer, resp.Body)
}

func Init(urlPrefix string) *gin.Engine {
	r := gin.Default()
	r.MaxMultipartMemory = 4 << 20
	// 允许跨域
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
	})
	// 静态访问
	staticFs, err := fs.Sub(f, "static")
	if err != nil {
		log.Print("read static files error")
	}
	// 因为gin打死不修bug，只能这样访问index.html
	r.StaticFileFS("/", "./", http.FS(staticFs))
	r.StaticFileFS("/favicon.ico", "./favicon.ico", http.FS(staticFs))
	r.StaticFS("/static", http.FS(staticFs))

	// url prefix
	root := r.Group(urlPrefix)

	// fb0透传 用于Linux驱动显示器
	display.RegisterFBRoutes(root)

	r.StaticFileFS("/alarm.html", "./alarm.html", http.FS(staticFs))
	r.StaticFileFS("/playlist.html", "./playlist.html", http.FS(staticFs))
	r.StaticFileFS("/eq.html", "./eq.html", http.FS(staticFs))
	// 回显配置时拷贝一份，把cookie清掉，避免局域网里直接看到登录态
	root.GET("/cfg.json", func(c *gin.Context) {
		cfg := conf.Cfg
		cfg.Cookie = ""
		c.JSON(http.StatusOK, cfg)
	})
	root.StaticFile("/weather.mp3", "./weather.mp3")

	// 网易云 API 流式代理。仅允许明确列出的 API 路径，避免代理被滥用为通用请求器。
	root.GET("/neapi/*path", func(c *gin.Context) {
		path := c.Param("path")
		allowed := path == "/user/info" || path == "/user/playlist" || path == "/v6/playlist/detail" || path == "/v2/discovery/recommend/songs"
		if !allowed {
			c.JSON(http.StatusNotFound, gin.H{"error": "unsupported netease api"})
			return
		}
		upstreamURL := "https://music.163.com/api" + path
		if rawQuery := c.Request.URL.RawQuery; rawQuery != "" {
			upstreamURL += "?" + rawQuery
		}
		// os=pc 用于请求完整歌单列表
		cookie := conf.Cfg.Cookie
		if cookie == "" {
			cookie = "os=pc"
		} else if !strings.Contains(cookie, "os=") {
			cookie += "; os=pc"
		}
		proxyStream(c, http.MethodGet, upstreamURL, "", map[string]string{"Cookie": cookie})
	})

	// QQ音乐歌单详情，参数只用 id
	root.GET("/qqapi/playlist/detail", func(c *gin.Context) {
		id := c.Query("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id is empty"})
			return
		}
		target := "https://c.y.qq.com/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg?type=1&json=1&utf8=1&onlysong=0&format=json" +
			"&disstid=" + url.QueryEscape(id) +
			"&g_tk=5381&loginUin=0&hostUin=0&inCharset=utf8&outCharset=utf-8&notice=0&platform=yqq.json&needNewCode=0"
		proxyStream(c, http.MethodGet, target, "", map[string]string{
			"Referer": "https://y.qq.com/",
			"Cookie":  conf.Cfg.Cookie,
		})
	})
	// QQ音乐用户信息，只从cookie里取QQ号，不请求上游
	root.GET("/qqapi/user/info", func(c *gin.Context) {
		uin := nemusic.QQCookieUin(conf.Cfg.Cookie)
		if uin == "" {
			c.JSON(http.StatusOK, gin.H{"code": 301, "message": "未登录"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"uin": uin}})
	})
	// QQ音乐用户歌单，按uin查，响应原样返回
	root.GET("/qqapi/user/playlist", func(c *gin.Context) {
		uin := c.Query("uin")
		if uin == "" {
			uin = nemusic.QQCookieUin(conf.Cfg.Cookie)
		}
		if uin == "" {
			c.JSON(http.StatusOK, gin.H{"code": 301, "message": "未登录"})
			return
		}
		body := `{"comm":{"ct":24,"cv":0,"format":"json","uin":"` + uin + `"},"req_0":{"module":"music.musicasset.PlaylistBaseRead","method":"GetPlaylistByUin","param":{"uin":"` + uin + `","offset":0,"size":200}}}`
		proxyStream(c, http.MethodPost, "https://u.y.qq.com/cgi-bin/musicu.fcg", body, map[string]string{
			"Referer":      "https://y.qq.com/",
			"Cookie":       conf.Cfg.Cookie,
			"Content-Type": "application/json",
		})
	})

	// 酷狗歌单信息与歌曲列表
	kugouHeader := func() map[string]string {
		return map[string]string{
			"Referer": "https://www.kugou.com/",
			"Cookie":  conf.Cfg.Cookie,
		}
	}
	root.GET("/kgapi/special/detail", func(c *gin.Context) {
		id := c.Query("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id is empty"})
			return
		}
		name := nemusic.KugouPlaylistName(id)
		if name == "" {
			name = "酷狗歌单" + id
		}
		c.JSON(http.StatusOK, gin.H{"status": 1, "data": gin.H{"specialname": name, "imgurl": ""}})
	})
	root.GET("/kgapi/special/song", func(c *gin.Context) {
		id := c.Query("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id is empty"})
			return
		}
		page := c.DefaultQuery("page", "1")
		// appid 和 mid 不带会直接返回 Access Deny
		target := "https://mobileservice.kugou.com/api/v3/special/song?specialid=" + url.QueryEscape(id) +
			"&page=" + url.QueryEscape(page) + "&pagesize=-1&plat=0&version=9108&appid=1005&clientver=9108&mid=" + url.QueryEscape(nemusic.KugouMid(conf.Cfg.Cookie))
		proxyStream(c, http.MethodGet, target, "", kugouHeader())
	})
	// 酷狗用户信息，只从cookie里取userid
	root.GET("/kgapi/user/info", func(c *gin.Context) {
		userID := nemusic.KugouCookieUserID(conf.Cfg.Cookie)
		if userID == "" {
			c.JSON(http.StatusOK, gin.H{"status": 0, "err_code": 301, "error": "未登录"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": 1, "data": gin.H{"userid": userID}})
	})
	root.GET("/kgapi/user/playlist", func(c *gin.Context) {
		userID := c.Query("userid")
		if userID == "" {
			userID = nemusic.KugouCookieUserID(conf.Cfg.Cookie)
		}
		if userID == "" {
			c.JSON(http.StatusOK, gin.H{"status": 0, "err_code": 301, "error": "未登录"})
			return
		}
		page := c.DefaultQuery("page", "1")
		target := "https://mobileservice.kugou.com/api/v3/user/playlist?userid=" + url.QueryEscape(userID) +
			"&page=" + url.QueryEscape(page) + "&pagesize=-1&plat=0&version=9108"
		proxyStream(c, http.MethodGet, target, "", kugouHeader())
	})

	// 切换音乐来源，只改一个字段，不动其它设置
	root.GET("/setMusicApi", func(c *gin.Context) {
		api := c.Query("api")
		if !conf.SetMusicApi(api) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的来源", "api": conf.Cfg.MusicApi})
			return
		}
		log.Println("已切换音乐来源", conf.Cfg.MusicApi)
		c.JSON(http.StatusOK, gin.H{"ok": true, "api": conf.Cfg.MusicApi})
	})

	// 保存登录cookie，playlist.html 登录时调用，传空值即清除
	// 三个平台共用一个字段，切换来源后要重新粘贴
	setCookie := func(c *gin.Context) {
		cookie := c.PostForm("cookie")
		if cookie == "" {
			cookie = c.Query("cookie")
		}
		cookie = strings.TrimSpace(cookie)
		conf.Cfg.Cookie = cookie
		conf.Save()
		// 不打印cookie内容，只打长度
		log.Println("已更新cookie，来源", conf.Cfg.MusicApi, "长度", len(cookie))
		c.JSON(http.StatusOK, gin.H{"code": 200, "cookieLen": len(cookie)})
	}
	root.POST("/setCookie", setCookie)
	root.GET("/setCookie", setCookie)

	// 允许直接浏览缓存，同时支持 WebDAV 上传/删除，当个小网盘用
	if conf.Cfg.SavePath != "" {
		root.StaticFS("/music", gin.Dir(conf.Cfg.SavePath, true))
		RegisterWebDAV(root, "/music", conf.Cfg.SavePath)
	}
	root.StaticFS("/sdcard", gin.Dir("/sdcard", true))
	RegisterWebDAV(root, "/sdcard", "/sdcard")

	// 删除缓存目录
	root.GET("/delSave", func(c *gin.Context) {
		if conf.Cfg.SavePath != "" && conf.Cfg.SavePath != "/" && conf.Cfg.SavePath != "./" {
			os.RemoveAll(conf.Cfg.SavePath)
			os.MkdirAll(conf.Cfg.SavePath, os.ModePerm)
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>已删除</h1>"+js2home))
		} else {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>你的操作很危险，驳回</h1><br>"+conf.Cfg.SavePath+js2home))
		}
	})

	root.GET("/hello", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Hello World!",
		})
	})

	root.GET("/prev", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h2>"+player.Prev()+"</h2>"+js2home))
	})

	root.GET("/next", func(c *gin.Context) {
		// c.JSON(200, gin.H{
		// 	"message": player.Next(),
		// })
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h2>"+player.Next()+"</h2>"+js2home))
	})

	root.GET("/stop", func(c *gin.Context) {
		player.Stop()
		// c.JSON(200, gin.H{
		// 	"message": "stop",
		// })
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>stop</h1>"+js2home))
	})

	root.GET("/play", func(c *gin.Context) {
		url := c.Query("url")
		if url == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>url is empty</h1>"+js2home))
			// 没有参数时一键播放
			if player.IsStop {
				player.Me1Key()
			} else if player.IsPaused {
				player.Resume()
			} else {
				player.Pause()
			}
			return
		}
		loopMode := c.Query("loopMode") != ""
		player.LoopMode = loopMode
		player.PlayUrl(url)
		s := "<h2>播放" + url + "</h2>"
		if loopMode {
			s += "<h1>单曲循环</h1>"
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(s+js2home))
	})

	// 播放实时 PCM 流, 仅在 Linux 上可用
	aplayStream := func(c *gin.Context) {
		if conf.IsApp {
			// 现在Android也可以用了,但端口不一样
			url := "http://" + c.Request.Host + c.Request.URL.RequestURI()
			url = strings.Replace(url, conf.Port, ":8880", 1)
			c.Redirect(http.StatusFound, url)
			return
		}
		rate := 44100
		channels := 2
		format := c.DefaultQuery("format", "s16le")
		if value := c.Query("rate"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				log.Println("aplay: invalid rate")
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rate"})
				return
			}
			rate = parsed
		}
		if value := c.Query("channels"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				log.Println("aplay: invalid channels")
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channels"})
				return
			}
			channels = parsed
		}
		if format != "s16le" {
			log.Println("aplay: unsupported format")
			c.JSON(http.StatusBadRequest, gin.H{"error": "only s16le is supported"})
			return
		}
		if rate < 8000 || rate > 192000 || channels < 1 || channels > 8 {
			log.Println("aplay: invalid rate or channels")
			c.JSON(http.StatusBadRequest, gin.H{"error": "rate must be 8000-192000 and channels 1-8"})
			return
		}
		// 收紧这条连接的内核接收缓冲，延迟攒得更少，反压也来得更早
		player.TuneStreamConn(c.Request.RemoteAddr)
		if err := player.PlayPCMStream(c.Request.Body, rate, channels, c.Request.RemoteAddr); err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, player.ErrPCMStreamUnsupported) {
				status = http.StatusNotImplemented
			}
			log.Println("aplay: error playing PCM stream", err)
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
	root.POST("/aplay", aplayStream)
	root.PUT("/aplay", aplayStream)
	// 实时流的延迟指标：本机排队(内核缓冲+管道+声卡)、跳读量、追赶次数、声卡欠载次数。
	root.GET("/aplayinfo", func(c *gin.Context) {
		c.JSON(http.StatusOK, player.PCMStreamStats())
	})

	// 一键急停按钮 自动控制播放停止
	root.GET("/1key", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(player.Me1Key()))
	})

	root.GET("/playlist", func(c *gin.Context) {
		id := nemusic.ParseID("playlist", c.Query("id"))
		if id == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>id is empty</h1>"+js2home))
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>播放歌单 "+player.PlayNePlaylist(id, c.Query("random") == "1")+"</h1>"+js2home))
	})

	// 由前端解析播放列表
	root.GET("/playmusiclist", func(c *gin.Context) {
		ids := c.Query("ids")
		if ids == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>ids is empty</h1>"+js2home))
			return
		}
		player.PlayNeMusicList(strings.Split(ids, ","), c.Query("id"), c.Query("random") == "1")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("ok"))
	})

	root.GET("/playmusic", func(c *gin.Context) {
		id := nemusic.ParseID("song", c.Query("id"))
		if id == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>id is empty</h1>"+js2home))
			return
		}
		loopMode := c.Query("loopMode") != ""
		player.PlayNeMusic(id, loopMode)
		s := "<h1>播放歌曲" + id + "</h1>"
		if loopMode {
			s += "<h1>单曲循环</h1>"
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(s+js2home))
	})

	root.GET("/vol", func(c *gin.Context) {
		vol := c.Query("vol")
		if vol == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>vol=1~100</h1>"+js2home))
			return
		}
		player.SetVol(vol)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("ok"+js2home))
	})

	root.GET("/echo", func(c *gin.Context) {
		msg := c.Query("msg")
		if msg == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>msg is empty</h1>"+js2home))
			return
		}
		app.Send(msg)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("ok"+js2home))
	})

	// 暂停播放
	root.GET("/pause", func(c *gin.Context) {
		player.Pause()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 恢复播放
	root.GET("/resume", func(c *gin.Context) {
		player.Resume()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 音量加
	root.GET("/volp", func(c *gin.Context) {
		player.VolUp()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})
	root.GET("/volpn", func(c *gin.Context) {
		// 暂停了这就变成下一首按钮了
		if player.IsPaused {
			player.Next()
		} else {
			player.VolUp()
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 音量减
	root.GET("/volm", func(c *gin.Context) {
		player.VolDown()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})
	root.GET("/volmp", func(c *gin.Context) {
		// 暂停了这就变成上一首按钮了
		if player.IsPaused {
			player.Prev()
		} else {
			player.VolDown()
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 测试闹钟
	root.GET("/testAlarm", func(c *gin.Context) {
		player.PlayAlarm()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>闹钟时间到</h1>"+js2home))
	})

	// 均衡器：查看当前设置
	// GET /eq
	root.GET("/eq", func(c *gin.Context) {
		c.JSON(http.StatusOK, conf.Cfg.Eq)
	})

	// 均衡器：实时调节（三段：低音/中音/高音，外加虚拟低音）
	// GET /eqset?enabled=1&bass=6&mid=-2&treble=3&preamp=0&vbass=30&vbassFreq=100
	// 只传需要改的参数，其余保持不变；改动立即生效并写回配置文件。
	root.GET("/eqset", func(c *gin.Context) {
		eq := conf.NormalizeEQ(conf.Cfg.Eq)

		if v := c.Query("enabled"); v != "" {
			eq.Enabled = v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "off")
		}
		if v := c.Query("preamp"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "preamp 需要数字，单位 dB"})
				return
			}
			eq.Preamp = f
		}
		// 虚拟低音：强度 0~100，频率是低频上限 Hz
		if v := c.Query("vbass"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "vbass 需要数字，0~100"})
				return
			}
			eq.VirtualBass = f
		}
		if v := c.Query("vbassFreq"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "vbassFreq 需要数字，单位 Hz"})
				return
			}
			eq.VirtualBassFreq = f
		}

		// 三段增益，参数名固定
		for _, item := range []struct {
			name string
			idx  int
		}{
			{"bass", 0},
			{"mid", 1},
			{"treble", 2},
		} {
			v := c.Query(item.name)
			if v == "" {
				continue
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": item.name + " 需要数字，单位 dB"})
				return
			}
			eq.Bands[item.idx].Gain = f
		}

		// 频点可选，不传就保持默认
		if v := c.Query("bassFreq"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "bassFreq 需要数字，单位 Hz"})
				return
			}
			eq.Bands[0].Freq = f
		}
		if v := c.Query("midFreq"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "midFreq 需要数字，单位 Hz"})
				return
			}
			eq.Bands[1].Freq = f
		}
		if v := c.Query("midQ"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "midQ 需要数字"})
				return
			}
			eq.Bands[1].Q = f
		}
		if v := c.Query("trebleFreq"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "trebleFreq 需要数字，单位 Hz"})
				return
			}
			eq.Bands[2].Freq = f
		}

		// 统一夹取范围(虚拟低音强度/频率)，避免越界参数写进配置
		eq = conf.NormalizeEQ(eq)
		conf.Cfg.Eq = eq
		player.ApplyEQConfig(eq)
		conf.Save()
		c.JSON(http.StatusOK, gin.H{"ok": true, "eq": eq, "active": player.EQActive()})
	})

	// 均衡器：关闭
	root.GET("/eqoff", func(c *gin.Context) {
		eq := conf.Cfg.Eq
		eq.Enabled = false
		conf.Cfg.Eq = eq
		player.ApplyEQConfig(eq)
		conf.Save()
		c.JSON(http.StatusOK, gin.H{"ok": true, "enabled": false})
	})

	// 加闹钟
	root.GET("/addAlarm", func(c *gin.Context) {
		hhmm := c.Query("hhmm")
		typeS := c.Query("type")
		// 万一呢 顺手就输进去了 手贱过一次导致闹钟没响
		hhmm = strings.ReplaceAll(strings.ReplaceAll(hhmm, "：", ""), ":", "")
		if len(typeS) > 0 && typeS[len(typeS)-1] == ',' {
			typeS = typeS[:len(typeS)-1]
		}
		if typeS == "" {
			// 默认一次
			typeS = "3"
		}
		if hhmm == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>hhmm is empty</h1>"+js2home))
			return
		}
		if typeList, exists := conf.Cfg.Alarm[hhmm]; exists {
			conf.Cfg.Alarm[hhmm] = append(typeList, strings.Split(typeS, ",")...)
		} else {
			conf.Cfg.Alarm[hhmm] = strings.Split(typeS, ",")
		}
		conf.Save()
		updateAppAlarmWake()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 删闹钟
	root.GET("/delAlarm", func(c *gin.Context) {
		hhmm := c.Query("hhmm")
		if hhmm == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>hhmm is empty</h1>"+js2home))
			return
		}
		delete(conf.Cfg.Alarm, hhmm)
		conf.Save()
		updateAppAlarmWake()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 跳过闹钟
	root.GET("/skipAlarm", func(c *gin.Context) {
		n := c.Query("n")
		if n == "0" {
			player.SkipAlarm = 0
		} else if n == "1" {
			player.SkipAlarm++
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>将跳过 "+strconv.Itoa(player.SkipAlarm)+" 次闹钟</h1>"+js2home))
	})

	// 更新设置
	root.GET("/updateCfg", func(c *gin.Context) {
		nePlayListId := c.Query("nePlayListId")
		defPlayListId := c.Query("defPlayListId")
		volAlarm := c.Query("volAlarm")
		VolDefault := c.Query("volDefault")
		Tz := c.Query("tz")
		WeatherCityCode := c.Query("weatherCityCode")
		WeatherUpdate := c.Query("weatherUpdate")
		wakelock := c.Query("wakelock")
		alarmTime := c.Query("alarmTime")
		muteWhenStop := c.Query("muteWhenStop")
		musicQuality := c.Query("musicQuality")
		savePath := c.Query("savePath")
		cacheLimit := c.Query("cacheLimit")
		defSeek := c.Query("defSeek")
		broadcastMode := c.Query("broadcastMode")
		smallWeekDate := c.Query("smallWeekDate")
		musicApi := c.Query("musicApi")
		var cacheLimitMB int64
		if cacheLimit != "" {
			var err error
			cacheLimitMB, err = strconv.ParseInt(cacheLimit, 10, 64)
			if err != nil || cacheLimitMB <= 0 || cacheLimitMB > (1<<63-1)/(1024*1024) {
				c.Data(http.StatusBadRequest, "text/html; charset=utf-8", []byte("<h1>缓存上限必须是正整数</h1>"+js2home))
				return
			}
		}
		if nePlayListId != "" {
			conf.Cfg.NePlayListId = nePlayListId
		}
		if defPlayListId != "" {
			conf.Cfg.DefPlayListId = defPlayListId
		}
		if volAlarm != "" {
			conf.Cfg.VolAlarm = volAlarm
		}
		if VolDefault != "" {
			conf.Cfg.VolDefault = VolDefault
		}
		if Tz != "" {
			tz, err := strconv.Atoi(Tz)
			if err != nil {
				c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>时区不是整数</h1>"+js2home))
				return
			} else {
				conf.Cfg.Tz = tz
				time.Local = time.FixedZone("UTC+", tz*3600)
			}
		}
		conf.Cfg.WeatherCityCode = WeatherCityCode
		conf.Cfg.WeatherUpdate = WeatherUpdate
		conf.Cfg.Wakelock = wakelock == "1"
		conf.Cfg.MuteWhenStop = muteWhenStop == "1"
		conf.Cfg.BroadcastMode = broadcastMode == "1"
		conf.Cfg.DefSeek = defSeek
		if conf.IsApp {
			app.SendLocal("DSEEK " + defSeek)
		}
		if alarmTime != "" {
			conf.Cfg.AlarmTime, _ = strconv.ParseFloat(alarmTime, 64)
		}
		conf.Cfg.MusicQuality = musicQuality
		// 换源只在传了合法值时生效，没传就保持原样，不清空其它设置
		if musicApi != "" && conf.IsValidMusicApi(musicApi) && conf.Cfg.MusicApi != musicApi {
			conf.Cfg.MusicApi = musicApi
			// 另一个平台的已播放记录没意义，清掉
			conf.Cfg.NePlayed = []string{}
		}
		if cacheLimit != "" {
			conf.Cfg.CacheLimit = cacheLimitMB
		}
		if savePath == "" {
			conf.Cfg.SavePath = ""
		} else if savePath != "/" && savePath != "./" {
			conf.Cfg.SavePath = savePath
			if conf.IsApp && conf.Cfg.SavePath[0] != '/' && conf.Cfg.SavePath[0] != '.' && (len(conf.Cfg.SavePath) == 1 || conf.Cfg.SavePath[1] != ':') {
				conf.Cfg.SavePath = "./" + conf.Cfg.SavePath
			}
			if !strings.HasSuffix(conf.Cfg.SavePath, "/") {
				conf.Cfg.SavePath += "/"
			}
		} else {
			conf.Cfg.SavePath = "./music/"
		}
		if conf.Cfg.SavePath != "" {
			err := os.MkdirAll(conf.Cfg.SavePath, os.ModePerm)
			if err != nil {
				log.Println("创建缓存目录出错", conf.Cfg.SavePath, err)
				c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>创建缓存目录出错"+conf.Cfg.SavePath+"</h1>"))
			}
		}
		if conf.Cfg.SmallWeekDate != smallWeekDate {
			conf.Cfg.SmallWeekDate = smallWeekDate
			if smallWeekDate != "" {
				conf.WorkDayApi()
			}
		}
		conf.Save()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 上传配置
	root.POST("/uploadCfg", func(c *gin.Context) {
		// 接收上传的file并保存
		file, _ := c.FormFile("file")
		if file == nil {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>file is empty</h1>"+js2home))
			return
		}
		c.SaveUploadedFile(file, "workdayAlarmClock.json")
		// 重新加载配置
		conf.Init()
		conf.WorkDayApi()
		updateAppAlarmWake()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>上传成功</h1>"+js2home))
	})

	// 上传兜底的mp3
	root.POST("/uploadMp3", func(c *gin.Context) {
		// 接收上传的file并保存
		file, _ := c.FormFile("file")
		if file == nil {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>file is empty</h1>"+js2home))
			return
		}
		c.SaveUploadedFile(file, "music.mp3")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>上传成功</h1>"+js2home))
	})

	// 删除上传的音乐使用默认兜底
	root.GET("/deleteMp3", func(c *gin.Context) {
		os.Remove("music.mp3")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>已删除</h1>"+js2home))
	})

	// 播放兜底音乐
	root.GET("/music.mp3", func(c *gin.Context) {
		if _, err := os.Stat("music.mp3"); os.IsNotExist(err) {
			c.FileFromFS("music.mp3", http.FS(staticFs))
		} else {
			c.File("music.mp3")
		}
	})

	// 当前状态
	root.GET("/status", func(c *gin.Context) {
		batLevel, _ := os.ReadFile("/sys/class/power_supply/battery/capacity")
		c.JSON(200, gin.H{
			"isStop":     player.IsStop,
			"isPaused":   player.IsPaused,
			"playList":   player.PlayList,
			"playListId": player.PlayListId,
			"isAlarm":    player.IsAlarm,
			"nowUrl":     player.NowUrl,
			"prevUrl":    player.PrevUrl,
			"batLevel":   string(batLevel),
			"nowId":      player.NowId,
			"startUnix":  player.StartUnix,
			"stopUnix":   player.StopUnix,
			"skipAlarm":  player.SkipAlarm,
			// 当前是否运行在 Android App 里，index 页面据此显示不同按钮
			"isApp": conf.IsApp,
			// 当前版本号
			"version": update.Current(),
		})
	})

	// 天气api
	root.GET("/getWeatherCityCode", func(c *gin.Context) {
		q := c.Query("q")
		code, _, err := weather.GetCityCode(q)
		if err != nil {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(err.Error()))
		} else {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(code))
		}
	})

	root.GET("/getWeather", func(c *gin.Context) {
		code := c.Query("code")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(weather.GetWeather(code)))
	})

	root.GET("/downWeather", func(c *gin.Context) {
		player.DownWeather()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>下载完毕</h1>"+js2home))
	})

	// 检查更新，只查不下载
	root.GET("/checkUpdate", func(c *gin.Context) {
		p, err := update.Check()
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, p)
	})

	// 更新到最新版本：下载替换后重启自己，所以要先把结果回给浏览器
	root.GET("/selfUpdate", func(c *gin.Context) {
		msg := func(s string) {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>"+s+"</h1>"+js2home))
		}
		if conf.IsApp {
			msg("Android 版请在 App 内更新")
			return
		}
		p, err := update.Check()
		if err != nil {
			msg("检查更新失败：" + err.Error())
			return
		}
		if !p.HasUpdate {
			msg("已是最新版本 v" + p.Current)
			return
		}
		if p.Asset == "" {
			msg("Release 里没有适合当前设备的文件，请手动下载")
			return
		}
		if player.IsAlarm || !player.IsStop {
			msg("正在播放，停止后再更新")
			return
		}
		msg("正在更新到 v" + p.Latest + "，下载完成后会自动重启，请稍候")
		go func() {
			// 留点时间把响应发出去，exec 会直接接管进程
			time.Sleep(2 * time.Second)
			if err := update.Apply(p); err != nil {
				log.Println("更新失败", err)
				return
			}
			log.Println("更新完成，正在重启")
			if err := update.Restart(); err != nil {
				log.Println("重启失败，请手动重启程序", err)
			}
		}()
	})

	root.GET("/restart", func(c *gin.Context) {
		// 做不到的，因为要运行完这个方法才会返回
		// c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
		if conf.IsApp {
			app.Send("RESTART")
			os.Exit(0)
		} else {
			// 统一用 update.Restart，它执行的是启动时缓存的路径，
			// 不会因为更新时把程序改名成 .old 而跑回旧版本
			if err := update.Restart(); err != nil {
				log.Println("重启失败", err)
				c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>重启失败</h1>"+js2home))
			}
		}
	})

	// 自动停止
	root.GET("/timeStop", func(c *gin.Context) {
		unix := c.Query("unix")
		min := c.Query("min")
		if unix != "" {
			player.StopUnix, err = strconv.ParseInt(unix, 10, 64)
			if err != nil {
				c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("unix不是整数"))
				return
			}
		} else if min != "" {
			minF, err := strconv.ParseFloat(min, 64)
			if err != nil {
				c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("min不是浮点"))
				return
			}
			player.StopUnix = time.Now().Unix() + int64(minF*60)
		} else {
			player.StopUnix = 0
		}
		if player.StopUnix == 0 {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h2>定时停止已取消</h2>"+js2home))
		} else {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h2>将在 "+time.Unix(player.StopUnix, 0).Format("2006-01-02 15:04:05")+" 后停止</h2>"+js2home))
		}
	})

	return r
}

// 更新app中每分钟锁的开关状态
func updateAppAlarmWake() {
	if conf.IsApp {
		if len(conf.Cfg.Alarm) > 0 {
			app.Send("ALARMON")
		} else {
			app.Send("ALARMOFF")
		}
	}
}

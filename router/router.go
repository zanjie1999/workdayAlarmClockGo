/*
 * http服务路由 文档https://gin-gonic.com/zh-cn/docs/
 * zyyme 202305023
 * v1.0
 */

package router

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
	"workdayAlarmClock/conf"
	"workdayAlarmClock/player"
	"workdayAlarmClock/weather"

	"github.com/gin-gonic/gin"
)

// 下面这个注释配置了需要打包进二进制文件的静态文件
//
//go:embed static/*
var f embed.FS

var (
	js2home = "\n<script>setInterval(function(){window.history.go(-1)},3000);</script>"
	js2back = "<script>window.history.go(-1)</script>"
)

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

	r.StaticFileFS("/alarm.html", "./alarm.html", http.FS(staticFs))
	root.StaticFile("/cfg.json", "./workdayAlarmClock.json")
	root.StaticFile("/weather.mp3", "./weather.mp3")

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
			return
		}
		player.PlayUrl(url)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h2>播放"+url+"</h2>"+js2home))
	})

	root.GET("/playlist", func(c *gin.Context) {
		id := c.Query("id")
		if id == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>id is empty</h1>"+js2home))
			return
		}
		player.PlayPlaylist(id, c.Query("random") == "1")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>播放歌单"+id+"</h1>"+js2home))
	})

	root.GET("/echo", func(c *gin.Context) {
		msg := c.Query("msg")
		if msg == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>msg is empty</h1>"+js2home))
			return
		}
		fmt.Println(msg)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("ok"))
	})

	// app暂停播放
	root.GET("/pause", func(c *gin.Context) {
		fmt.Println("PAUSE")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// app恢复播放
	root.GET("/resume", func(c *gin.Context) {
		fmt.Println("RESUME")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 音量加
	root.GET("/volp", func(c *gin.Context) {
		fmt.Println("VOLP")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 音量减
	root.GET("/volm", func(c *gin.Context) {
		fmt.Println("VOLM")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
	})

	// 测试闹钟
	root.GET("/testAlarm", func(c *gin.Context) {
		player.PlayAlarm()
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>闹钟时间到</h1>"+js2home))
	})

	// 加闹钟
	root.GET("/addAlarm", func(c *gin.Context) {
		hhmm := c.Query("hhmm")
		typeS := c.Query("type")
		if hhmm == "" || typeS == "" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>hhmm or type is empty</h1>"+js2home))
			return
		}
		conf.Cfg.Alarm[hhmm] = int(typeS[0] - '0')
		conf.Save()
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
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
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
		log.Println(wakelock)
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
			"isStop":    player.IsStop,
			"playList":  player.PlayList,
			"isAlarm":   player.IsAlarm,
			"nowUrl":    player.NowUrl,
			"prevUrl":   player.PrevUrl,
			"batLevel":  string(batLevel),
			"nowId":     player.NowId,
			"startUnix": player.StartUnix,
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

	root.GET("/restart", func(c *gin.Context) {
		// 做不到的，因为要运行完这个方法才会返回
		// c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(js2back))
		fmt.Println("RESTART")
		os.Exit(0)
	})

	return r
}

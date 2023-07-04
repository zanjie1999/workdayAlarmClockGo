/*
 * http服务路由 文档https://gin-gonic.com/zh-cn/docs/
 * zyyme 202305023
 * v1.0
 */

package router

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"workdayAlarmClock/player"

	"github.com/gin-gonic/gin"
)

// 下面这个注释配置了需要打包进二进制文件的静态文件
//
//go:embed static/*
var f embed.FS

var js2home = "\n<script>setInterval(function(){window.location.href=document.referrer},3000);</script>"

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

	root.GET("/hello", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Hello World!",
		})
	})

	root.GET("/next", func(c *gin.Context) {
		// c.JSON(200, gin.H{
		// 	"message": player.Next(),
		// })
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>"+player.Next()+"</h1>"+js2home))
	})

	root.GET("/stop", func(c *gin.Context) {
		player.Stop()
		// c.JSON(200, gin.H{
		// 	"message": "stop",
		// })
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte("<h1>stop</h1>"+js2home))
	})

	return r
}

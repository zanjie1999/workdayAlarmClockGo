//go:build !linux || app

package display

import (
	"io"
	"net/http"
	"strings"
	"workdayAlarmClock/conf"

	"github.com/gin-gonic/gin"
)

type Framebuffer struct{}

func ReadInfo() (Info, error)       { return Info{}, ErrUnsupported }
func Open() (*Framebuffer, error)   { return nil, ErrUnsupported }
func (f *Framebuffer) Close() error { return nil }
func (f *Framebuffer) StreamJPEG(io.Reader, string) (StreamStats, error) {
	return StreamStats{}, ErrUnsupported
}

func RegisterFBRoutes(root *gin.RouterGroup) {
	root.GET("/fbinfo", func(c *gin.Context) {
		if conf.IsApp {
			// 现在Android也可以用了,但端口不一样
			url := "http://" + c.Request.Host + c.Request.URL.RequestURI()
			url = strings.Replace(url, conf.Port, ":8880", 1)
			c.Redirect(http.StatusFound, url)
			return
		}
		c.JSON(http.StatusNotImplemented, gin.H{"error": ErrUnsupported.Error()})
	})
	fb := func(c *gin.Context) {
		if conf.IsApp {
			// 现在Android也可以用了,但端口不一样
			url := "http://" + c.Request.Host + c.Request.URL.RequestURI()
			url = strings.Replace(url, conf.Port, ":8880", 1)
			c.Redirect(http.StatusFound, url)
			return
		}
		c.JSON(http.StatusNotImplemented, gin.H{"error": ErrUnsupported.Error()})
	}
	root.POST("/fb", fb)
	root.PUT("/fb", fb)
}

//go:build !linux

package display

import (
	"io"
	"net/http"

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
		c.JSON(http.StatusNotImplemented, gin.H{"error": ErrUnsupported.Error()})
	})
	fb := func(c *gin.Context) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": ErrUnsupported.Error()})
	}
	root.POST("/fb", fb)
	root.PUT("/fb", fb)
}

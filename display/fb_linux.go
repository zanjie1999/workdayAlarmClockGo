//go:build linux

package display

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"github.com/gin-gonic/gin"
)

const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
	fbioPanDisplay     = 0x4606
	maxJPEGFrameSize   = 8 << 20
	maxFBFrameSize     = 128 << 20
)

type fbVarScreeninfo struct {
	Xres, Yres, XresVirtual, YresVirtual                        uint32
	Xoffset, Yoffset, BitsPerPixel, Grayscale                   uint32
	Red, Green, Blue, Transp                                    Bitfield
	Nonstd, Activate, Height, Width, AccelFlags                 uint32
	Pixclock, LeftMargin, RightMargin, UpperMargin, LowerMargin uint32
	HsyncLen, VsyncLen, Sync, Vmode, Rotate, Colorspace         uint32
	Reserved                                                    [4]uint32
}

type fbFixScreeninfo struct {
	ID                                 [16]byte
	SmemStart                          uintptr
	SmemLen                            uint32
	Type, TypeAux, Visual              uint32
	Xpanstep, Ypanstep, Ywrapstep, Pad uint16
	LineLength                         uint32
	MmioStart                          uintptr
	MmioLen, Accel                     uint32
	Capabilities                       uint16
	Reserved                           [2]uint16
}

type Framebuffer struct {
	file         *os.File
	info         Info
	varInfo      fbVarScreeninfo
	writeBuffer  []byte
	frontPage    int
	doubleBuffer bool
}

func fbIoctl(fd uintptr, cmd uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, cmd, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func readScreenInfo(file *os.File) (Info, fbVarScreeninfo, fbFixScreeninfo, error) {
	var v fbVarScreeninfo
	if err := fbIoctl(file.Fd(), fbioGetVScreenInfo, unsafe.Pointer(&v)); err != nil {
		return Info{}, v, fbFixScreeninfo{}, fmt.Errorf("FBIOGET_VSCREENINFO: %w", err)
	}
	var f fbFixScreeninfo
	if err := fbIoctl(file.Fd(), fbioGetFScreenInfo, unsafe.Pointer(&f)); err != nil {
		return Info{}, v, f, fmt.Errorf("FBIOGET_FSCREENINFO: %w", err)
	}
	if v.Xres == 0 || v.Yres == 0 || f.LineLength == 0 || v.BitsPerPixel == 0 {
		return Info{}, v, f, errors.New("invalid framebuffer geometry")
	}
	frameSize := uint64(f.LineLength) * uint64(v.Yres)
	if frameSize == 0 || frameSize > maxFBFrameSize {
		return Info{}, v, f, fmt.Errorf("framebuffer frame is too large: %d bytes", frameSize)
	}
	if uint64(f.SmemLen) < frameSize {
		return Info{}, v, f, fmt.Errorf("framebuffer memory is too small: %d < %d", f.SmemLen, frameSize)
	}
	return Info{
		Device: DevicePath, Width: v.Xres, Height: v.Yres, WidthVirtual: v.XresVirtual, HeightVirtual: v.YresVirtual,
		BitsPerPixel: v.BitsPerPixel, Format: detectFBFormat(&v), Stride: f.LineLength, FrameSize: frameSize, MemorySize: f.SmemLen,
		YPanStep: f.Ypanstep, RedOffset: v.Red.Offset, RedLength: v.Red.Length, GreenOffset: v.Green.Offset, GreenLength: v.Green.Length,
		BlueOffset: v.Blue.Offset, BlueLength: v.Blue.Length, AlphaOffset: v.Transp.Offset, AlphaLength: v.Transp.Length,
	}, v, f, nil
}

func detectFBFormat(v *fbVarScreeninfo) string {
	if v.BitsPerPixel == 24 && v.Red.Offset == 16 && v.Red.Length == 8 && v.Green.Offset == 8 && v.Green.Length == 8 && v.Blue.Offset == 0 && v.Blue.Length == 8 {
		return "bgr888"
	}
	if v.BitsPerPixel == 16 && v.Red.Offset == 11 && v.Red.Length == 5 && v.Green.Offset == 5 && v.Green.Length == 6 && v.Blue.Offset == 0 && v.Blue.Length == 5 {
		return "rgb565"
	}
	if v.BitsPerPixel == 32 && v.Red.Offset == 16 && v.Red.Length == 8 && v.Green.Offset == 8 && v.Green.Length == 8 && v.Blue.Offset == 0 && v.Blue.Length == 8 {
		if v.Transp.Length == 8 && v.Transp.Offset == 24 {
			return "argb8888"
		}
		return "xrgb8888"
	}
	if v.BitsPerPixel == 32 && v.Red.Offset == 0 && v.Red.Length == 8 && v.Green.Offset == 8 && v.Green.Length == 8 && v.Blue.Offset == 16 && v.Blue.Length == 8 {
		if v.Transp.Length == 8 && v.Transp.Offset == 24 {
			return "bgra8888"
		}
		return "bgrx8888"
	}
	return "custom"
}

func ReadInfo() (Info, error) {
	file, err := os.OpenFile(DevicePath, os.O_RDWR, 0)
	if err != nil {
		return Info{}, err
	}
	defer file.Close()
	info, _, _, err := readScreenInfo(file)
	if err != nil {
		return Info{}, err
	}
	if info.Format == "custom" {
		return info, fmt.Errorf("unsupported framebuffer format: %s", info.Format)
	}
	return info, nil
}

func Open() (*Framebuffer, error) {
	file, err := os.OpenFile(DevicePath, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	info, v, _, err := readScreenInfo(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if info.Format == "custom" {
		file.Close()
		return nil, fmt.Errorf("unsupported framebuffer format: %s", info.Format)
	}
	if info.BitsPerPixel != 16 && info.BitsPerPixel != 24 && info.BitsPerPixel != 32 {
		file.Close()
		return nil, fmt.Errorf("unsupported framebuffer bpp: %d", info.BitsPerPixel)
	}
	fb := &Framebuffer{file: file, info: info, varInfo: v}
	fb.writeBuffer = make([]byte, int(info.FrameSize))
	if uint64(info.MemorySize) >= info.FrameSize*2 && uint64(info.HeightVirtual) >= uint64(info.Height)*2 {
		page := 0
		if info.Height > 0 && v.Yoffset >= info.Height {
			page = int(v.Yoffset / info.Height)
			if page > 1 {
				page = 0
			}
		}
		v.Yoffset = uint32(page) * info.Height
		if err := fbIoctl(file.Fd(), fbioPanDisplay, unsafe.Pointer(&v)); err == nil {
			fb.frontPage, fb.doubleBuffer, fb.varInfo = page, true, v
			fb.info.DoubleBuffer = true
		} else {
			log.Printf("framebuffer: double buffering unavailable, using single buffer: %v", err)
		}
	}
	return fb, nil
}

func (fb *Framebuffer) Close() error {
	if fb.file == nil {
		return nil
	}
	err := fb.file.Close()
	fb.file = nil
	return err
}

func (fb *Framebuffer) StreamJPEG(body io.Reader, boundary string) (StreamStats, error) {
	if boundary == "" {
		return StreamStats{}, errors.New("missing multipart boundary")
	}
	reader := multipart.NewReader(bufio.NewReaderSize(body, 32<<10), boundary)
	var stats StreamStats
	var jpegBuf bytes.Buffer
	for {
		part, err := reader.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if stats.Frames == 0 {
					return stats, errors.New("empty MJPEG stream")
				}
				return stats, nil
			}
			return stats, fmt.Errorf("read MJPEG part: %w", err)
		}
		if part.FormName() != "" {
			continue
		}
		contentType := part.Header.Get("Content-Type")
		if contentType != "" && contentType != "image/jpeg" {
			_, _ = io.Copy(io.Discard, part)
			continue
		}
		jpegBuf.Reset()
		n, err := io.Copy(&jpegBuf, io.LimitReader(part, maxJPEGFrameSize+1))
		if err != nil {
			return stats, fmt.Errorf("read JPEG frame: %w", err)
		}
		if n > maxJPEGFrameSize {
			return stats, fmt.Errorf("JPEG frame is too large: %d bytes", n)
		}
		img, err := jpeg.Decode(bytes.NewReader(jpegBuf.Bytes()))
		if err != nil {
			return stats, fmt.Errorf("decode JPEG: %w", err)
		}
		bounds := img.Bounds()
		if bounds.Dx() != int(fb.info.Width) ||
			bounds.Dy() != int(fb.info.Height) {
			return stats, fmt.Errorf("jpeg size mismatch %dx%d", bounds.Dx(), bounds.Dy())
		}
		if err := fb.writeImage(img); err != nil {
			return stats, fmt.Errorf("write framebuffer: %w", err)
		}
		stats.Frames++
		stats.JPEGBytes += uint64(n)
	}
}

func (fb *Framebuffer) writeImage(img image.Image) error {
	if !fb.doubleBuffer {
		return fb.writePage(img, 0)
	}

	backPage := 1 - fb.frontPage
	if err := fb.writePage(img, backPage); err != nil {
		return err
	}

	v := fb.varInfo
	v.Yoffset = uint32(backPage) * fb.info.Height

	// 先写完后台页，再切换显示页，避免显示未完成的画面。
	if err := fbIoctl(
		fb.file.Fd(),
		fbioPanDisplay,
		unsafe.Pointer(&v),
	); err != nil {
		return err
	}

	fb.varInfo = v
	fb.frontPage = backPage

	return nil
}

func (fb *Framebuffer) writePage(img image.Image, page int) error {
	frameSize := int(fb.info.FrameSize)

	dst := fb.writeBuffer
	if len(dst) != frameSize {
		return errors.New("invalid framebuffer buffer")
	}

	clear(dst)

	if err := encodeImage(
		img,
		dst,
		int(fb.info.Width),
		int(fb.info.Height),
		int(fb.info.Stride),
		int(fb.info.BitsPerPixel),
		fb.info.Format,
	); err != nil {
		return err
	}

	// Some fb drivers keep write state across frames on one descriptor. The
	// original shell renderer reopens /dev/fb0 for every frame, so do the same.
	writer, err := os.OpenFile(DevicePath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open framebuffer for writing: %w", err)
	}
	defer writer.Close()
	if _, err := writer.Seek(int64(page)*int64(frameSize), io.SeekStart); err != nil {
		return fmt.Errorf("reset framebuffer write offset: %w", err)
	}
	return writeFull(writer, dst)
}

func writeFullAt(file *os.File, p []byte, offset int64) error {
	for len(p) > 0 {
		n, err := file.WriteAt(p, offset)
		if n > 0 {
			offset += int64(n)
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func writeFull(file *os.File, p []byte) error {
	for len(p) > 0 {
		n, err := file.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func encodeImage(img image.Image, dst []byte, width, height, stride, bpp int, format string) error {
	if width <= 0 || height <= 0 || stride <= 0 {
		return errors.New("invalid framebuffer dimensions")
	}
	switch {
	case bpp == 32 && (format == "xrgb8888" || format == "argb8888"):
		encode32(img, dst, width, height, stride, false)
	case bpp == 32 && (format == "bgrx8888" || format == "bgra8888"):
		encode32(img, dst, width, height, stride, true)
	case bpp == 24 && format == "bgr888":
		encode24(img, dst, width, height, stride)
	case bpp == 16 && format == "rgb565":
		encode16(img, dst, width, height, stride)
	default:
		return fmt.Errorf("unsupported framebuffer format %s/%dbpp", format, bpp)
	}
	return nil
}

func encode24(img image.Image, dst []byte, width, height, stride int) {
	for y := 0; y < height; y++ {
		row := dst[y*stride:]
		for x := 0; x < width; x++ {
			r, g, b := rgbAt(img, x, y)
			i := x * 3
			row[i], row[i+1], row[i+2] = b, g, r
		}
	}
}

func encode32(img image.Image, dst []byte, width, height, stride int, rgbMemory bool) {
	for y := 0; y < height; y++ {
		row := dst[y*stride:]
		for x := 0; x < width; x++ {
			r, g, b := rgbAt(img, x, y)
			i := x * 4
			if rgbMemory {
				row[i], row[i+1], row[i+2] = r, g, b
			} else {
				row[i], row[i+1], row[i+2] = b, g, r
			}
			row[i+3] = 0xff
		}
	}
}
func encode16(img image.Image, dst []byte, width, height, stride int) {
	for y := 0; y < height; y++ {
		row := dst[y*stride:]
		for x := 0; x < width; x++ {
			r, g, b := rgbAt(img, x, y)
			v := uint16(r>>3)<<11 | uint16(g>>2)<<5 | uint16(b>>3)
			i := x * 2
			row[i], row[i+1] = byte(v), byte(v>>8)
		}
	}
}
func rgbAt(img image.Image, x, y int) (uint8, uint8, uint8) {
	switch src := img.(type) {
	case *image.YCbCr:
		yi := src.YOffset(x+src.Rect.Min.X, y+src.Rect.Min.Y)
		ci := src.COffset(x+src.Rect.Min.X, y+src.Rect.Min.Y)
		r, g, b := color.YCbCrToRGB(src.Y[yi], src.Cb[ci], src.Cr[ci])
		return r, g, b
	case *image.RGBA:
		i := src.PixOffset(x+src.Rect.Min.X, y+src.Rect.Min.Y)
		return src.Pix[i], src.Pix[i+1], src.Pix[i+2]
	case *image.NRGBA:
		i := src.PixOffset(x+src.Rect.Min.X, y+src.Rect.Min.Y)
		return src.Pix[i], src.Pix[i+1], src.Pix[i+2]
	case *image.Gray:
		v := src.GrayAt(x+src.Rect.Min.X, y+src.Rect.Min.Y).Y
		return v, v, v
	default:
		r, g, b, _ := img.At(x+img.Bounds().Min.X, y+img.Bounds().Min.Y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
	}
}

type fbStreamSession struct {
	body io.ReadCloser
	done chan struct{}
}

var (
	fbStreamMu     sync.Mutex
	activeFBStream *fbStreamSession
)

func replaceFBStream(body io.ReadCloser) *fbStreamSession {
	session := &fbStreamSession{body: body, done: make(chan struct{})}
	fbStreamMu.Lock()
	old := activeFBStream
	activeFBStream = session
	fbStreamMu.Unlock()
	if old != nil {
		_ = old.body.Close()
		<-old.done
	}
	return session
}

func finishFBStream(session *fbStreamSession) {
	fbStreamMu.Lock()
	if activeFBStream == session {
		activeFBStream = nil
	}
	fbStreamMu.Unlock()
	close(session.done)
}

func fbStream(c *gin.Context) {
	session := replaceFBStream(c.Request.Body)
	defer finishFBStream(session)

	boundary, err := parseMJPEGBoundary(c.GetHeader("Content-Type"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fb, err := Open()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrUnsupported) {
			status = http.StatusNotImplemented
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	defer fb.Close()

	stats, err := fb.StreamJPEG(c.Request.Body, boundary)
	if err != nil {
		if stats.Frames == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "frames": stats.Frames, "jpeg_bytes": stats.JPEGBytes})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "frames": stats.Frames, "jpeg_bytes": stats.JPEGBytes})
}

func parseMJPEGBoundary(contentType string) (string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", errors.New("invalid Content-Type; expected multipart/x-mixed-replace with a boundary")
	}
	if mediaType != "multipart/x-mixed-replace" && mediaType != "multipart/mixed" {
		return "", errors.New("Content-Type must be multipart/x-mixed-replace")
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", errors.New("missing MJPEG multipart boundary")
	}
	return boundary, nil
}

func RegisterFBRoutes(root *gin.RouterGroup) {
	root.GET("/fbinfo", func(c *gin.Context) {
		info, err := ReadInfo()
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, ErrUnsupported) {
				status = http.StatusNotImplemented
			}
			c.JSON(status, gin.H{"error": err.Error(), "info": info})
			return
		}
		c.JSON(http.StatusOK, info)
	})
	root.POST("/fb", fbStream)
	root.PUT("/fb", fbStream)
}

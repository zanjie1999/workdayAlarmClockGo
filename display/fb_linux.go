//go:build linux

package display

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
	"workdayAlarmClock/player"

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
	mapped       []byte
	pageMode     [2]uint8 // 0 unknown, 1 stream write, 2 mmap
	frontPage    int
	doubleBuffer bool
}

const (
	pageModeUnknown = iota
	pageModeWrite
	pageModeMmap
)

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
	// Some grayscale/e-ink framebuffers expose one 8-bit pixel while
	// reporting identical RGB bitfields. Treat that layout as gray8.
	if v.BitsPerPixel == 8 &&
		v.Red.Offset == 0 && v.Red.Length == 8 &&
		v.Green.Offset == 0 && v.Green.Length == 8 &&
		v.Blue.Offset == 0 && v.Blue.Length == 8 &&
		v.Transp.Length == 0 {
		return "gray8"
	}
	if v.BitsPerPixel == 8 && v.Grayscale != 0 {
		return "gray8"
	}
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
	if info.BitsPerPixel != 8 && info.BitsPerPixel != 16 && info.BitsPerPixel != 24 && info.BitsPerPixel != 32 {
		file.Close()
		return nil, fmt.Errorf("unsupported framebuffer bpp: %d", info.BitsPerPixel)
	}
	fb := &Framebuffer{file: file, info: info, varInfo: v}
	fb.writeBuffer = make([]byte, int(info.FrameSize))
	if info.MemorySize > 0 {
		mapped, mapErr := syscall.Mmap(int(file.Fd()), 0, int(info.MemorySize), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if mapErr == nil {
			fb.mapped = mapped
		} else {
			log.Printf("framebuffer: mmap unavailable, using stream writes: %v", mapErr)
		}
	}
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

	// If we were using page flipping, always leave the framebuffer on page 0.
	// Some drivers keep the last Yoffset after the writer exits, which makes the
	// next user of /dev/fb0 appear to start from an empty/old page.
	if fb.doubleBuffer {
		v := fb.varInfo
		v.Yoffset = 0
		if err := fbIoctl(fb.file.Fd(), fbioPanDisplay, unsafe.Pointer(&v)); err != nil {
			log.Printf("framebuffer: restore first page on close failed: %v", err)
		}
		fb.varInfo = v
		fb.frontPage = 0
	}

	if fb.mapped != nil {
		_ = syscall.Munmap(fb.mapped)
		fb.mapped = nil
	}
	err := fb.file.Close()
	fb.file = nil
	return err
}

type jpegFrame struct {
	buf bytes.Buffer
}

var jpegFramePool sync.Pool

const maxPooledJPEGBuffer = 256 << 10

func acquireJPEGFrame() *jpegFrame {
	if value := jpegFramePool.Get(); value != nil {
		frame := value.(*jpegFrame)
		frame.buf.Reset()
		return frame
	}
	frame := &jpegFrame{}
	frame.buf.Grow(16 << 10)
	return frame
}

func releaseJPEGFrame(frame *jpegFrame) {
	if frame == nil {
		return
	}
	// Keep the pool bounded so an occasional large JPEG does not retain an
	// 8 MiB backing array for the lifetime of the process.
	if frame.buf.Cap() > maxPooledJPEGBuffer {
		frame.buf = bytes.Buffer{}
	} else {
		frame.buf.Reset()
	}
	jpegFramePool.Put(frame)
}

// publishLatestFrame keeps at most one unread frame. For screen mirroring,
// stale frames are latency, not useful work.
func publishLatestFrame(ch chan *jpegFrame, frame *jpegFrame) {
	select {
	case ch <- frame:
		return
	default:
	}

	// There is already a frame waiting for the renderer. Replace it with the
	// newest one instead of blocking the network reader.
	select {
	case old := <-ch:
		releaseJPEGFrame(old)
	default:
	}

	select {
	case ch <- frame:
	default:
		releaseJPEGFrame(frame)
	}
}

func readJPEGFrames(ctx context.Context, body io.Reader, boundary string, latest chan *jpegFrame) error {
	if boundary == "" {
		return errors.New("missing multipart boundary")
	}

	reader := multipart.NewReader(body, boundary)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		part, err := reader.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read MJPEG part: %w", err)
		}

		if part.FormName() != "" {
			continue
		}

		contentType := part.Header.Get("Content-Type")
		if contentType != "" && contentType != "image/jpeg" {
			_, _ = io.Copy(io.Discard, part)
			continue
		}

		frame := acquireJPEGFrame()
		n, err := io.Copy(&frame.buf, io.LimitReader(part, maxJPEGFrameSize+1))
		if err != nil {
			releaseJPEGFrame(frame)
			return fmt.Errorf("read JPEG frame: %w", err)
		}
		if n > maxJPEGFrameSize {
			releaseJPEGFrame(frame)
			return fmt.Errorf("JPEG frame is too large: %d bytes", n)
		}

		publishLatestFrame(latest, frame)
	}
}

// StreamJPEGContext separates network ingestion from JPEG decode/framebuffer
// rendering. The reader keeps consuming the MJPEG stream while the renderer
// works; only the newest unread frame is retained.
func (fb *Framebuffer) StreamJPEG(body io.Reader, boundary string) (StreamStats, error) {
	return fb.StreamJPEGContext(context.Background(), body, boundary)
}

func (fb *Framebuffer) StreamJPEGContext(ctx context.Context, body io.Reader, boundary string) (StreamStats, error) {
	if boundary == "" {
		return StreamStats{}, errors.New("missing multipart boundary")
	}

	latest := make(chan *jpegFrame, 1)
	readerResult := make(chan error, 1)

	go func() {
		err := readJPEGFrames(ctx, body, boundary, latest)
		readerResult <- err
		close(latest)
	}()

	// Explicitly close the request body on cancellation so replacing an active
	// stream can unblock the multipart reader promptly.
	stopCloseWatcher := make(chan struct{})
	if closer, ok := body.(io.Closer); ok {
		go func() {
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-stopCloseWatcher:
			}
		}()
	}
	defer close(stopCloseWatcher)

	var stats StreamStats
	for {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()

		case frame, ok := <-latest:
			if !ok {
				if err := <-readerResult; err != nil {
					return stats, err
				}
				if stats.Frames == 0 {
					return stats, errors.New("empty MJPEG stream")
				}
				return stats, nil
			}

			frameBytes := uint64(frame.buf.Len())
			img, err := jpeg.Decode(bytes.NewReader(frame.buf.Bytes()))
			releaseJPEGFrame(frame)
			if err != nil {
				return stats, fmt.Errorf("decode JPEG: %w", err)
			}

			if err := ctx.Err(); err != nil {
				return stats, err
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
			stats.JPEGBytes += frameBytes
		}
	}
}

func (fb *Framebuffer) writeImage(img image.Image) error {
	if !fb.doubleBuffer {
		return fb.writePage(img, 0)
	}

	backPage := 1 - fb.frontPage
	if err := fb.writePage(img, backPage); err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			// Some drivers expose two virtual pages but reject writes at the
			// second page offset. Fall back to the single visible page.
			fb.doubleBuffer = false
			fb.info.DoubleBuffer = false
			v := fb.varInfo
			v.Yoffset = 0
			_ = fbIoctl(fb.file.Fd(), fbioPanDisplay, unsafe.Pointer(&v))
			fb.varInfo = v
			fb.frontPage = 0
			return fb.writePage(img, 0)
		}
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
	if page < 0 || page >= len(fb.pageMode) {
		return errors.New("invalid framebuffer page")
	}
	if fb.pageMode[page] == pageModeUnknown {
		fb.pageMode[page] = fb.probePage(page)
	}

	// A few framebuffer drivers expose enough memory for mmap but do not treat
	// writes through the mapped pointer as a complete framebuffer update. They
	// may update small regions or delay dirty tracking for a very long time.
	// For single-buffer mode prefer the tested write(2) path so the whole frame
	// is committed in one operation. Double buffering can still use mmap because
	// it writes a hidden page and flips it atomically.
	if !fb.doubleBuffer && fb.pageMode[page] == pageModeMmap {
		fb.pageMode[page] = pageModeWrite
	}

	var dst []byte
	if fb.pageMode[page] == pageModeMmap {
		offset := page * frameSize
		if offset < 0 || offset+frameSize > len(fb.mapped) {
			fb.pageMode[page] = pageModeWrite
		} else {
			dst = fb.mapped[offset : offset+frameSize]
		}
	}
	if dst == nil {
		dst = fb.writeBuffer
		if len(dst) != frameSize {
			return errors.New("invalid framebuffer buffer")
		}
	}

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

	if fb.pageMode[page] == pageModeMmap {
		if player.ShellPlayer == "kindle" {
			return exec.Command("/usr/sbin/eips", "").Run()
		}
		return nil
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
	if err := writeFull(writer, dst); err != nil {
		return err
	}
	if player.ShellPlayer == "kindle" {
		return exec.Command("/usr/sbin/eips", "").Run()
	}
	return nil
}

// probePage uses the established write path to verify that this page can be
// addressed before enabling mmap. Some drivers advertise two virtual pages
// but reject writes to the second page with ENOSPC.
func (fb *Framebuffer) probePage(page int) uint8 {
	if len(fb.mapped) == 0 {
		return pageModeWrite
	}
	offset := int64(page) * int64(fb.info.FrameSize)
	writer, err := os.OpenFile(DevicePath, os.O_WRONLY, 0)
	if err != nil {
		return pageModeWrite
	}
	defer writer.Close()
	if _, err = writer.WriteAt([]byte{0}, offset); err != nil {
		log.Printf("framebuffer: page %d stream probe failed, keeping stream writes: %v", page, err)
		return pageModeWrite
	}
	return pageModeMmap
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

	// jpeg.Decode returns *image.YCbCr for JPEG input. Keep this path separate
	// from the generic image.Image path so the inner pixel loop never performs
	// an interface type switch or calls img.At.
	if src, ok := img.(*image.YCbCr); ok {
		switch {
		case bpp == 8 && format == "gray8":
			encodeYCbCr8(src, dst, width, height, stride)
		case bpp == 32 && (format == "xrgb8888" || format == "argb8888"):
			encodeYCbCr32(src, dst, width, height, stride, false)
		case bpp == 32 && (format == "bgrx8888" || format == "bgra8888"):
			encodeYCbCr32(src, dst, width, height, stride, true)
		case bpp == 24 && format == "bgr888":
			encodeYCbCr24(src, dst, width, height, stride)
		case bpp == 16 && format == "rgb565":
			encodeYCbCr16(src, dst, width, height, stride)
		default:
			return fmt.Errorf("unsupported framebuffer format %s/%dbpp", format, bpp)
		}
		return nil
	}

	switch {
	case bpp == 8 && format == "gray8":
		encode8(img, dst, width, height, stride)
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

func encodeYCbCr8(src *image.YCbCr, dst []byte, width, height, stride int) {
	minX, minY := src.Rect.Min.X, src.Rect.Min.Y
	for y := 0; y < height; y++ {
		yi := src.YOffset(minX, y+minY)
		copy(dst[y*stride:y*stride+width], src.Y[yi:yi+width])
	}
}

// ycbcrHorizontalStep returns the number of luma pixels represented by one
// chroma sample. Vertical subsampling is handled by COffset once per row.
func ycbcrHorizontalStep(ratio image.YCbCrSubsampleRatio) int {
	switch ratio {
	case image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420:
		return 2
	case image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410:
		return 4
	default:
		return 1
	}
}

// ycbcrToRGBFast is the same integer conversion used by image/color, kept
// local so the compiler can inline it directly into the framebuffer loops.
func ycbcrToRGBFast(y, cb, cr uint8) (uint8, uint8, uint8) {
	yy1 := int32(y) * 0x10101
	cb1 := int32(cb) - 128
	cr1 := int32(cr) - 128

	r := yy1 + 91881*cr1
	if uint32(r)&0xff000000 == 0 {
		r >>= 16
	} else {
		r = ^(r >> 31)
	}

	g := yy1 - 22554*cb1 - 46802*cr1
	if uint32(g)&0xff000000 == 0 {
		g >>= 16
	} else {
		g = ^(g >> 31)
	}

	b := yy1 + 116130*cb1
	if uint32(b)&0xff000000 == 0 {
		b >>= 16
	} else {
		b = ^(b >> 31)
	}

	return uint8(r), uint8(g), uint8(b)
}

func encodeYCbCr24(src *image.YCbCr, dst []byte, width, height, stride int) {
	minX, minY := src.Rect.Min.X, src.Rect.Min.Y
	chromaStep := ycbcrHorizontalStep(src.SubsampleRatio)

	for y := 0; y < height; y++ {
		absY := y + minY
		yi := src.YOffset(minX, absY)
		ci := src.COffset(minX, absY)
		yRow := src.Y[yi : yi+width]
		row := dst[y*stride : y*stride+width*3]
		chromaIndex := ci
		absX := minX

		for x := 0; x < width; x, absX = x+1, absX+1 {
			r, g, b := ycbcrToRGBFast(yRow[x], src.Cb[chromaIndex], src.Cr[chromaIndex])
			i := x * 3
			row[i], row[i+1], row[i+2] = b, g, r

			if chromaStep == 1 || (absX+1)%chromaStep == 0 {
				chromaIndex++
			}
		}
	}
}

func encodeYCbCr32(src *image.YCbCr, dst []byte, width, height, stride int, rgbMemory bool) {
	minX, minY := src.Rect.Min.X, src.Rect.Min.Y
	chromaStep := ycbcrHorizontalStep(src.SubsampleRatio)

	for y := 0; y < height; y++ {
		absY := y + minY
		yi := src.YOffset(minX, absY)
		ci := src.COffset(minX, absY)
		yRow := src.Y[yi : yi+width]
		row := dst[y*stride : y*stride+width*4]
		chromaIndex := ci
		absX := minX

		for x := 0; x < width; x, absX = x+1, absX+1 {
			r, g, b := ycbcrToRGBFast(yRow[x], src.Cb[chromaIndex], src.Cr[chromaIndex])
			i := x * 4
			if rgbMemory {
				row[i], row[i+1], row[i+2] = r, g, b
			} else {
				row[i], row[i+1], row[i+2] = b, g, r
			}
			row[i+3] = 0xff

			if chromaStep == 1 || (absX+1)%chromaStep == 0 {
				chromaIndex++
			}
		}
	}
}

func encodeYCbCr16(src *image.YCbCr, dst []byte, width, height, stride int) {
	minX, minY := src.Rect.Min.X, src.Rect.Min.Y
	chromaStep := ycbcrHorizontalStep(src.SubsampleRatio)

	for y := 0; y < height; y++ {
		absY := y + minY
		yi := src.YOffset(minX, absY)
		ci := src.COffset(minX, absY)
		yRow := src.Y[yi : yi+width]
		row := dst[y*stride : y*stride+width*2]
		chromaIndex := ci
		absX := minX

		for x := 0; x < width; x, absX = x+1, absX+1 {
			r, g, b := ycbcrToRGBFast(yRow[x], src.Cb[chromaIndex], src.Cr[chromaIndex])
			v := uint16(r>>3)<<11 | uint16(g>>2)<<5 | uint16(b>>3)
			i := x * 2
			row[i], row[i+1] = byte(v), byte(v>>8)

			if chromaStep == 1 || (absX+1)%chromaStep == 0 {
				chromaIndex++
			}
		}
	}
}

func encode8(img image.Image, dst []byte, width, height, stride int) {
	bounds := img.Bounds()
	minX, minY := bounds.Min.X, bounds.Min.Y

	for y := 0; y < height; y++ {
		row := dst[y*stride:]
		for x := 0; x < width; x++ {
			row[x] = grayAt(img, x+minX, y+minY)
		}
	}
}

func grayAt(img image.Image, x, y int) uint8 {
	switch src := img.(type) {
	case *image.YCbCr:
		i := src.YOffset(x, y)
		return src.Y[i]
	case *image.Gray:
		return src.GrayAt(x, y).Y
	case *image.Gray16:
		return uint8(src.Gray16At(x, y).Y >> 8)
	case *image.RGBA:
		i := src.PixOffset(x, y)
		return rgbToGray(src.Pix[i], src.Pix[i+1], src.Pix[i+2])
	case *image.NRGBA:
		i := src.PixOffset(x, y)
		return rgbToGray(src.Pix[i], src.Pix[i+1], src.Pix[i+2])
	default:
		r, g, b, _ := img.At(x, y).RGBA()
		return rgbToGray(uint8(r>>8), uint8(g>>8), uint8(b>>8))
	}
}

func rgbToGray(r, g, b uint8) uint8 {
	return uint8((299*uint32(r) + 587*uint32(g) + 114*uint32(b) + 500) / 1000)
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
		return ycbcrToRGBFast(src.Y[yi], src.Cb[ci], src.Cr[ci])
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
	streamCtx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	go func() {
		select {
		case <-session.done:
			cancel()
		case <-streamCtx.Done():
		}
	}()

	boundary, err := parseMJPEGBoundary(c.GetHeader("Content-Type"))
	if err != nil {
		fmt.Println("fbStream Error", err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fb, err := Open()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrUnsupported) {
			status = http.StatusNotImplemented
		}
		fmt.Println("fbStream Error", err.Error())
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	defer fb.Close()

	stats, err := fb.StreamJPEGContext(streamCtx, c.Request.Body, boundary)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if stats.Frames == 0 {
			fmt.Println("fbStream Error", err.Error())
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		} else {
			fmt.Println("fbStream Error", err.Error(), "frames", stats.Frames, "jpeg_bytes", stats.JPEGBytes)
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

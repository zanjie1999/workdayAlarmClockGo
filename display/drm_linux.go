//go:build linux && !app

package display

import (
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
	"unsafe"
)

const (
	drmModeConnected     = 1
	drmModeTypePreferred = 1 << 3
	drmModePageFlipEvent = 1 << 0
	drmModeConnectorDPI  = 17

	// DRM fourcc values, expressed from their printable four-character codes.
	// Only formats already supported by encodeImage are candidates here.
	drmFormatXRGB8888 = uint32('X') | uint32('R')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatARGB8888 = uint32('A') | uint32('R')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatXBGR8888 = uint32('X') | uint32('B')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatABGR8888 = uint32('A') | uint32('B')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatRGB888   = uint32('R') | uint32('G')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatBGR888   = uint32('B') | uint32('G')<<8 | uint32('2')<<16 | uint32('4')<<24
	drmFormatRGB565   = uint32('R') | uint32('G')<<8 | uint32('1')<<16 | uint32('6')<<24
	drmFormatBGR565   = uint32('B') | uint32('G')<<8 | uint32('1')<<16 | uint32('6')<<24

	// Linux generic ioctl encoding. Rockchip targets use the generic encoding.
	drmIOCDirNone      = uintptr(0)
	drmIOCDirWrite     = uintptr(1)
	drmIOCDirRead      = uintptr(2)
	drmIOCDirReadWrite = drmIOCDirRead | drmIOCDirWrite
	drmIOCNRBits       = uintptr(8)
	drmIOCTypeBits     = uintptr(8)
	drmIOCSizeBits     = uintptr(14)
	drmIOCNRShift      = uintptr(0)
	drmIOCTypeShift    = drmIOCNRShift + drmIOCNRBits
	drmIOCSizeShift    = drmIOCTypeShift + drmIOCTypeBits
	drmIOCDirShift     = drmIOCSizeShift + drmIOCSizeBits

	drmIOCTYpe = uintptr('d')
)

const (
	drmIoctlSetClientCap = 0x0D
	drmIoctlGetResources = 0xA0
	drmIoctlGetCRTC      = 0xA1
	drmIoctlSetCRTC      = 0xA2
	drmIoctlGetEncoder   = 0xA6
	drmIoctlGetConnector = 0xA7
	drmIoctlRMFB         = 0xAF
	drmIoctlPageFlip     = 0xB0
	drmIoctlCreateDumb   = 0xB2
	drmIoctlMapDumb      = 0xB3
	drmIoctlDestroyDumb  = 0xB4
	drmIoctlGetPlaneRes  = 0xB5
	drmIoctlGetPlane     = 0xB6

	drmIoctlSetMaster  = 0x1E
	drmIoctlDropMaster = 0x1F

	// DRM_CLIENT_CAP_UNIVERSAL_PLANES. Without this capability, legacy
	// GETPLANERESOURCES/GETPLANE may hide the primary plane. RV1106's active
	// scanout is exactly such a primary plane (VOP0-win1-0 / plane 55).
	drmClientCapUniversalPlanes = uint64(2)
)

type drmSetClientCap struct {
	Capability uint64
	Value      uint64
}

func drmIOC(dir, nr, size uintptr) uintptr {
	return (dir << drmIOCDirShift) |
		(size << drmIOCSizeShift) |
		(drmIOCTYpe << drmIOCTypeShift) |
		(nr << drmIOCNRShift)
}

func drmIO(nr uintptr) uintptr {
	return drmIOC(drmIOCDirNone, nr, 0)
}

func drmIOW(nr, size uintptr) uintptr {
	return drmIOC(drmIOCDirWrite, nr, size)
}

func drmIOWR(nr, size uintptr) uintptr {
	return drmIOC(drmIOCDirReadWrite, nr, size)
}

func drmIoctl(fd uintptr, cmd uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, cmd, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

type drmModeModeInfo struct {
	Clock      uint32
	HDisplay   uint16
	HSyncStart uint16
	HSyncEnd   uint16
	HTotal     uint16
	HSkew      uint16
	VDisplay   uint16
	VSyncStart uint16
	VSyncEnd   uint16
	VTotal     uint16
	VScan      uint16
	VRefresh   uint32
	Flags      uint32
	Type       uint32
	Name       [32]byte
}

type drmModeCardRes struct {
	FBIDPtr         uint64
	CRTCIDPtr       uint64
	ConnectorIDPtr  uint64
	EncoderIDPtr    uint64
	CountFBs        uint32
	CountCRTCs      uint32
	CountConnectors uint32
	CountEncoders   uint32
	MinWidth        uint32
	MaxWidth        uint32
	MinHeight       uint32
	MaxHeight       uint32
}

type drmModeCRTC struct {
	SetConnectorsPtr uint64
	CountConnectors  uint32
	CRTCID           uint32
	FBID             uint32
	X                uint32
	Y                uint32
	GammaSize        uint32
	ModeValid        uint32
	Mode             drmModeModeInfo
}

type drmModeGetEncoder struct {
	EncoderID      uint32
	EncoderType    uint32
	CRTCID         uint32
	PossibleCRTCS  uint32
	PossibleClones uint32
}

type drmModeGetConnector struct {
	EncodersPtr     uint64
	ModesPtr        uint64
	PropsPtr        uint64
	PropValuesPtr   uint64
	CountModes      uint32
	CountProps      uint32
	CountEncoders   uint32
	EncoderID       uint32
	ConnectorID     uint32
	ConnectorType   uint32
	ConnectorTypeID uint32
	Connection      uint32
	MMWidth         uint32
	MMHeight        uint32
	Subpixel        uint32
	Pad             uint32
}

type drmModeCreateDumb struct {
	Height uint32
	Width  uint32
	BPP    uint32
	Flags  uint32
	Handle uint32
	Pitch  uint32
	Size   uint64
}

type drmModeMapDumb struct {
	Handle uint32
	Pad    uint32
	Offset uint64
}

type drmModeDestroyDumb struct {
	Handle uint32
}

type drmModeGetPlaneRes struct {
	PlaneIDPtr  uint64
	CountPlanes uint32
}

type drmModeGetPlane struct {
	PlaneID          uint32
	CRTCID           uint32
	FBID             uint32
	PossibleCRTCS    uint32
	GammaSize        uint32
	CountFormatTypes uint32
	FormatTypePtr    uint64
}

type drmModeFB2 struct {
	FBID        uint32
	Width       uint32
	Height      uint32
	PixelFormat uint32
	Flags       uint32
	Handles     [4]uint32
	Pitches     [4]uint32
	Offsets     [4]uint32
	Modifier    [4]uint64
}

type drmModePageFlip struct {
	CRTCID   uint32
	FBID     uint32
	Flags    uint32
	Reserved uint32
	UserData uint64
}

type drmFBBuffer struct {
	handle uint32
	fbID   uint32
	pitch  uint32
	size   uint64
	mapped []byte
}

type drmFormatChoice struct {
	fourcc uint32
	name   string
	bpp    uint32
}

var drmFormatPreferences = []drmFormatChoice{
	{fourcc: drmFormatXRGB8888, name: "xrgb8888", bpp: 32},
	{fourcc: drmFormatARGB8888, name: "argb8888", bpp: 32},
	{fourcc: drmFormatXBGR8888, name: "bgrx8888", bpp: 32},
	{fourcc: drmFormatABGR8888, name: "bgra8888", bpp: 32},
	{fourcc: drmFormatRGB888, name: "rgb888", bpp: 24},
	{fourcc: drmFormatBGR888, name: "bgr888", bpp: 24},
	{fourcc: drmFormatRGB565, name: "rgb565", bpp: 16},
	{fourcc: drmFormatBGR565, name: "bgr565", bpp: 16},
}

type drmFramebuffer struct {
	file *os.File
	info Info

	crtcID      uint32
	connectorID uint32
	mode        drmModeModeInfo
	oldCRTC     drmModeCRTC
	oldFB       uint32
	oldValid    bool

	buffers     [2]drmFBBuffer
	frontPage   int
	pendingPage int
	flipPending bool

	statsFrames       uint64
	statsWait         time.Duration
	statsEncode       time.Duration
	statsFlip         time.Duration
	statsWindowStart  time.Time
	statsWindowFrames uint64
	statsWindowWait   time.Duration
	statsWindowEncode time.Duration
	statsWindowFlip   time.Duration
}

func (d *drmFramebuffer) Close() error {
	if d == nil || d.file == nil {
		return nil
	}

	var firstErr error

	// If our most recent flip is still pending, wait for it before touching
	// either dumb buffer or restoring the previous scanout. This keeps the
	// display from ever referencing a buffer that we are about to destroy.
	if d.flipPending {
		if err := d.waitForFlip(); err != nil {
			log.Printf("drm: wait for pending page flip on close failed: %v", err)
		}
	}

	_ = d.restoreCRTC()

	for i := range d.buffers {
		buf := &d.buffers[i]
		if buf.fbID != 0 {
			fbID := buf.fbID
			if err := drmIoctl(d.file.Fd(), drmIOWR(drmIoctlRMFB, unsafe.Sizeof(fbID)), unsafe.Pointer(&fbID)); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("remove drm framebuffer %d: %w", buf.fbID, err)
			}
			buf.fbID = 0
		}
		if buf.mapped != nil {
			if err := syscall.Munmap(buf.mapped); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("unmap drm buffer: %w", err)
			}
			buf.mapped = nil
		}
		if buf.handle != 0 {
			arg := drmModeDestroyDumb{Handle: buf.handle}
			if err := drmIoctl(d.file.Fd(), drmIOWR(drmIoctlDestroyDumb, unsafe.Sizeof(arg)), unsafe.Pointer(&arg)); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("destroy drm dumb buffer %d: %w", buf.handle, err)
			}
			buf.handle = 0
		}
	}

	if err := drmIoctl(d.file.Fd(), drmIO(drmIoctlDropMaster), nil); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("drop drm master: %w", err)
	}
	if err := d.file.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	d.file = nil
	return firstErr
}

func (d *drmFramebuffer) restoreCRTC() bool {
	if d.file == nil || d.crtcID == 0 || !d.oldValid || d.oldFB == 0 {
		return false
	}

	// When we attached to an already-active CRTC, restoring the previous FB
	// with a page flip avoids reconstructing connector/encoder topology. This
	// is important for fixed embedded panels, where connector enumeration can
	// be incomplete even though the CRTC is actively scanning out.
	if d.connectorID == 0 {
		flip := drmModePageFlip{CRTCID: d.crtcID, FBID: d.oldFB, Flags: drmModePageFlipEvent}
		if err := drmIoctl(d.file.Fd(), drmIOWR(drmIoctlPageFlip, unsafe.Sizeof(flip)), unsafe.Pointer(&flip)); err == nil {
			var event [32]byte
			if _, err := readFullFD(int(d.file.Fd()), event[:]); err == nil {
				return true
			}
		} else {
			log.Printf("drm: restore previous FB %d on CRTC %d failed: %v", d.oldFB, d.crtcID, err)
		}
	}

	if d.connectorID == 0 || d.oldCRTC.ModeValid == 0 {
		return false
	}
	connector := d.connectorID
	restore := d.oldCRTC
	restore.SetConnectorsPtr = uint64(uintptr(unsafe.Pointer(&connector)))
	restore.CountConnectors = 1
	if err := drmIoctl(d.file.Fd(), drmIOWR(drmIoctlSetCRTC, unsafe.Sizeof(restore)), unsafe.Pointer(&restore)); err != nil {
		log.Printf("drm: restore previous CRTC %d: %v", d.crtcID, err)
		return false
	}
	return true
}

func (d *drmFramebuffer) writeImage(img image.Image) error {
	return d.writeImageAt(img, 0, 0, false)
}

func (d *drmFramebuffer) writeImageAt(img image.Image, x, y int, clearFrame bool) error {
	// Double-buffer the scanout and deliberately overlap the next JPEG decode
	// with the previous page flip. The timing counters below help distinguish
	// CPU-side conversion from vblank pacing when tuning the renderer.
	var waitElapsed time.Duration
	if d.flipPending {
		waitStart := time.Now()
		if err := d.waitForFlip(); err != nil {
			return fmt.Errorf("wait for drm page flip: %w", err)
		}
		waitElapsed = time.Since(waitStart)
		d.statsWait += waitElapsed
		d.frontPage = d.pendingPage
		d.flipPending = false
	}

	backPage := 1 - d.frontPage
	buf := &d.buffers[backPage]
	encodeStart := time.Now()
	dst := buf.mapped
	width, height := int(d.info.Width), int(d.info.Height)
	if clearFrame {
		clear(dst)
		width, height = img.Bounds().Dx(), img.Bounds().Dy()
	}
	offset := y*int(d.info.Stride) + x*int(d.info.BitsPerPixel/8)
	if err := encodeImage(img, dst[offset:], width, height, int(d.info.Stride), int(d.info.BitsPerPixel), d.info.Format); err != nil {
		return err
	}
	encodeElapsed := time.Since(encodeStart)
	d.statsEncode += encodeElapsed

	flipStart := time.Now()
	flip := drmModePageFlip{CRTCID: d.crtcID, FBID: buf.fbID, Flags: drmModePageFlipEvent}
	if err := drmIoctl(d.file.Fd(), drmIOWR(drmIoctlPageFlip, unsafe.Sizeof(flip)), unsafe.Pointer(&flip)); err != nil {
		return fmt.Errorf("drm page flip: %w", err)
	}
	flipElapsed := time.Since(flipStart)
	d.statsFlip += flipElapsed
	d.pendingPage = backPage
	d.flipPending = true
	d.statsFrames++
	d.statsWindowFrames++
	d.statsWindowWait += waitElapsed
	d.statsWindowEncode += encodeElapsed
	d.statsWindowFlip += flipElapsed
	if d.statsWindowStart.IsZero() {
		d.statsWindowStart = time.Now()
	}
	if elapsed := time.Since(d.statsWindowStart); elapsed >= 10*time.Second {
		log.Printf("drm frame timing: fps=%.2f frames=%d avg_wait=%s avg_encode=%s avg_flip_submit=%s", float64(d.statsWindowFrames)/elapsed.Seconds(), d.statsWindowFrames, d.statsWindowWait/time.Duration(d.statsWindowFrames), d.statsWindowEncode/time.Duration(d.statsWindowFrames), d.statsWindowFlip/time.Duration(d.statsWindowFrames))
		d.statsWindowStart = time.Now()
		d.statsWindowFrames = 0
		d.statsWindowWait = 0
		d.statsWindowEncode = 0
		d.statsWindowFlip = 0
	}
	return nil
}

func (d *drmFramebuffer) waitForFlip() error {
	// PAGE_FLIP_EVENT is delivered on the DRM fd. Read exactly one kernel event.
	var event [32]byte
	if _, err := readFullFD(int(d.file.Fd()), event[:]); err != nil {
		return err
	}
	return nil
}

func readFullFD(fd int, p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := syscall.Read(fd, p[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}

func openDRM() (*drmFramebuffer, error) {
	paths := discoverDRMDevices()
	var lastErr error
	for _, path := range paths {
		d, err := openDRMDevice(path)
		if err == nil {
			return d, nil
		}
		lastErr = err
		log.Printf("drm: %s unavailable: %v", path, err)
	}
	if lastErr == nil {
		lastErr = errors.New("drm: no DRM card devices found")
	}
	return nil, lastErr
}

func discoverDRMDevices() []string {
	paths, err := filepath.Glob("/dev/dri/card*")
	if err != nil || len(paths) == 0 {
		return []string{"/dev/dri/card0"}
	}
	sort.Strings(paths)
	// Prefer card0 when present, then try every other KMS card. This avoids
	// binding to a render-only or auxiliary card when the display controller is
	// exposed under a different card number.
	for i, path := range paths {
		if path == "/dev/dri/card0" && i != 0 {
			copy(paths[1:i+1], paths[0:i])
			paths[0] = path
			break
		}
	}
	return paths
}

func openDRMDevice(devicePath string) (*drmFramebuffer, error) {
	file, err := os.OpenFile(devicePath, os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}

	closeWithError := func(err error) (*drmFramebuffer, error) {
		_ = file.Close()
		return nil, err
	}

	if err := drmIoctl(file.Fd(), drmIO(drmIoctlSetMaster), nil); err != nil {
		return closeWithError(fmt.Errorf("set drm master: %w", err))
	}

	cap := drmSetClientCap{Capability: drmClientCapUniversalPlanes, Value: 1}
	if err := drmIoctl(file.Fd(), drmIOW(drmIoctlSetClientCap, unsafe.Sizeof(cap)), unsafe.Pointer(&cap)); err != nil {
		log.Printf("drm: %s universal planes unavailable, continuing with legacy plane enumeration: %v", devicePath, err)
	} else {
		log.Printf("drm: %s universal planes enabled", devicePath)
	}

	resources, connectors, encoders, crtcs, err := drmGetResources(file.Fd())
	if err != nil {
		_ = drmIoctl(file.Fd(), drmIO(drmIoctlDropMaster), nil)
		return closeWithError(err)
	}
	log.Printf("drm: %s resources connectors=%d encoders=%d crtcs=%d fbs=%d", devicePath, resources.CountConnectors, resources.CountEncoders, resources.CountCRTCs, resources.CountFBs)

	crtcID, crtc, active := drmFindActiveCRTC(file.Fd(), crtcs)
	connectorID := uint32(0)
	var mode drmModeModeInfo
	var connectorMode drmModeModeInfo
	usedActivePlane := false
	currentPlaneFB := uint32(0)

	if active {
		mode = crtc.Mode
		connectorID = drmFindConnectorForCRTC(file.Fd(), connectors, crtcID)
		if connectorID != 0 {
			log.Printf("drm: reusing active CRTC %d with FB %d connector=%d mode=%s", crtcID, crtc.FBID, connectorID, drmModeName(mode))
		} else {
			// A few drivers omit the current connector relationship from the
			// encoder objects even though there is only one active output. Fall
			// back to the connector/mode with the strongest match.
			connectorID, connectorMode = drmFindUsableConnectorMode(file.Fd(), connectors)
			if connectorID != 0 && connectorMode.HDisplay == mode.HDisplay && connectorMode.VDisplay == mode.VDisplay {
				log.Printf("drm: active CRTC %d connector relationship incomplete, using connector=%d mode=%s", crtcID, connectorID, drmModeName(mode))
			} else {
				connectorID = 0
			}
		}
	} else {
		planeID, planeCRTC, planeFB, ok := drmFindActivePlane(file.Fd())
		if ok {
			crtcID = planeCRTC
			currentPlaneFB = planeFB
			crtc = drmModeCRTC{CRTCID: crtcID, FBID: planeFB}
			connectorID, mode = drmFindUsableConnectorMode(file.Fd(), connectors)
			if mode.HDisplay != 0 && mode.VDisplay != 0 {
				usedActivePlane = true
				log.Printf("drm: reusing active plane %d with CRTC %d FB %d connector=%d mode=%s", planeID, crtcID, planeFB, connectorID, drmModeName(mode))
			}
		}

		if !usedActivePlane {
			if cid, m, candidateCRTC, ok := drmFindEmbeddedPanel(file.Fd(), connectors, crtcs); ok {
				crtcID = candidateCRTC
				connectorID = cid
				mode = m
				crtc = drmModeCRTC{CRTCID: crtcID}
				if err := drmIoctl(file.Fd(), drmIOWR(drmIoctlGetCRTC, unsafe.Sizeof(crtc)), unsafe.Pointer(&crtc)); err != nil {
					// Some vendor drivers return an incomplete CRTC state. We already
					// have a valid mode and selected CRTC, so preserve the zero/partial
					// state instead of rejecting an otherwise usable embedded panel.
					log.Printf("drm: CRTC %d query after embedded-panel selection failed: %v", crtcID, err)
				}
				usedActivePlane = true
				log.Printf("drm: embedded-panel fallback: connector=%d crtc=%d mode=%s reported_fb=%d mode_valid=%d", connectorID, crtcID, drmModeName(mode), crtc.FBID, crtc.ModeValid)
			}
		}

		if !usedActivePlane {
			var err error
			connectorID, mode, crtcID, err = drmSelectDisplay(file.Fd(), connectors, encoders, crtcs)
			if err != nil {
				_ = drmIoctl(file.Fd(), drmIO(drmIoctlDropMaster), nil)
				return closeWithError(err)
			}
			crtc = drmModeCRTC{CRTCID: crtcID}
			if err := drmIoctl(file.Fd(), drmIOWR(drmIoctlGetCRTC, unsafe.Sizeof(crtc)), unsafe.Pointer(&crtc)); err != nil {
				_ = drmIoctl(file.Fd(), drmIO(drmIoctlDropMaster), nil)
				return closeWithError(fmt.Errorf("get current CRTC %d: %w", crtcID, err))
			}
		}
	}

	if mode.HDisplay == 0 || mode.VDisplay == 0 {
		_ = drmIoctl(file.Fd(), drmIO(drmIoctlDropMaster), nil)
		return closeWithError(fmt.Errorf("drm: no usable display mode on CRTC %d", crtcID))
	}

	// Query plane formats for the selected CRTC instead of assuming every DRM
	// device accepts XR24/XRGB8888. Prefer the formats already supported by our
	// image encoder, with XRGB8888 first for widest compatibility.
	_, planeFormats, ok := drmFindPlaneForCRTC(file.Fd(), crtcs, crtcID)
	if !ok {
		_ = drmIoctl(file.Fd(), drmIO(drmIoctlDropMaster), nil)
		return closeWithError(fmt.Errorf("drm: no plane compatible with CRTC %d", crtcID))
	}
	format, ok := drmChooseFormat(planeFormats)
	if !ok {
		_ = drmIoctl(file.Fd(), drmIO(drmIoctlDropMaster), nil)
		return closeWithError(fmt.Errorf("drm: CRTC %d has no supported scanout format", crtcID))
	}
	log.Printf("drm: selected format=%s bpp=%d fourcc=0x%08x for CRTC %d", format.name, format.bpp, format.fourcc, crtcID)

	previousFB := crtc.FBID
	if previousFB == 0 && currentPlaneFB != 0 {
		previousFB = currentPlaneFB
	}

	d := &drmFramebuffer{
		file:        file,
		info:        Info{Device: devicePath, Width: uint32(mode.HDisplay), Height: uint32(mode.VDisplay), WidthVirtual: uint32(mode.HDisplay), HeightVirtual: uint32(mode.VDisplay), BitsPerPixel: format.bpp, Format: format.name, DoubleBuffer: true},
		crtcID:      crtcID,
		connectorID: connectorID,
		mode:        mode,
		oldCRTC:     crtc,
		oldFB:       previousFB,
		oldValid:    previousFB != 0,
	}

	for i := range d.buffers {
		if err := drmCreateBuffer(file.Fd(), uint32(mode.HDisplay), uint32(mode.VDisplay), format, &d.buffers[i]); err != nil {
			_ = d.Close()
			return nil, err
		}
	}
	frameSize := uint64(d.buffers[0].pitch) * uint64(mode.VDisplay)
	if frameSize == 0 || frameSize > maxFBFrameSize {
		_ = d.Close()
		return nil, fmt.Errorf("drm framebuffer is too large: %d bytes", frameSize)
	}
	d.info.Stride = d.buffers[0].pitch
	d.info.FrameSize = frameSize
	if d.buffers[0].size > uint64(^uint32(0)) {
		d.info.MemorySize = ^uint32(0)
	} else {
		d.info.MemorySize = uint32(d.buffers[0].size)
	}

	if connectorID == 0 {
		_ = d.Close()
		return nil, fmt.Errorf("drm: connector required to activate CRTC %d", crtcID)
	}

	connector := connectorID
	set := drmModeCRTC{
		SetConnectorsPtr: uint64(uintptr(unsafe.Pointer(&connector))),
		CountConnectors:  1,
		CRTCID:           crtcID,
		FBID:             d.buffers[0].fbID,
		X:                0,
		Y:                0,
		ModeValid:        1,
		Mode:             mode,
	}
	if err := drmIoctl(file.Fd(), drmIOWR(drmIoctlSetCRTC, unsafe.Sizeof(set)), unsafe.Pointer(&set)); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("set drm CRTC %d framebuffer %d: %w", crtcID, d.buffers[0].fbID, err)
	}
	log.Printf("drm: initial SETCRTC device=%s connector=%d crtc=%d fb=%d mode=%s format=%s", devicePath, connectorID, crtcID, d.buffers[0].fbID, drmModeName(mode), format.name)

	return d, nil
}

func drmFindPlaneForCRTC(fd uintptr, crtcIDs []uint32, targetCRTC uint32) (uint32, []uint32, bool) {
	var res drmModeGetPlaneRes
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlaneRes, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil || res.CountPlanes == 0 {
		return 0, nil, false
	}
	planeIDs := make([]uint32, res.CountPlanes)
	res.PlaneIDPtr = uint64(uintptr(unsafe.Pointer(&planeIDs[0])))
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlaneRes, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil {
		return 0, nil, false
	}

	crtcIndex := -1
	for i, id := range crtcIDs {
		if id == targetCRTC {
			crtcIndex = i
			break
		}
	}

	var fallbackID uint32
	var fallbackFormats []uint32
	for _, planeID := range planeIDs {
		plane := drmModeGetPlane{PlaneID: planeID}
		if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlane, unsafe.Sizeof(plane)), unsafe.Pointer(&plane)); err != nil {
			continue
		}
		formats := make([]uint32, plane.CountFormatTypes)
		if len(formats) > 0 {
			plane.FormatTypePtr = uint64(uintptr(unsafe.Pointer(&formats[0])))
			if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlane, unsafe.Sizeof(plane)), unsafe.Pointer(&plane)); err != nil {
				continue
			}
		}

		compatible := plane.CRTCID == targetCRTC
		if !compatible && crtcIndex >= 0 && crtcIndex < 32 {
			compatible = plane.PossibleCRTCS&(1<<uint(crtcIndex)) != 0
		}
		if !compatible {
			continue
		}
		log.Printf("drm: format probe plane=%d crtc=%d current_fb=%d possible_crtcs=0x%x formats=%d", planeID, plane.CRTCID, plane.FBID, plane.PossibleCRTCS, len(formats))

		if plane.CRTCID == targetCRTC && plane.FBID != 0 {
			return planeID, formats, true
		}
		if fallbackID == 0 {
			fallbackID = planeID
			fallbackFormats = formats
		}
	}
	if fallbackID != 0 {
		return fallbackID, fallbackFormats, true
	}
	return 0, nil, false
}

func drmChooseFormat(formats []uint32) (drmFormatChoice, bool) {
	for _, preferred := range drmFormatPreferences {
		for _, supported := range formats {
			if supported == preferred.fourcc {
				return preferred, true
			}
		}
	}
	return drmFormatChoice{}, false
}

func drmFindActivePlane(fd uintptr) (uint32, uint32, uint32, bool) {
	var res drmModeGetPlaneRes
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlaneRes, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil {
		log.Printf("drm: get plane resources failed: %v", err)
		return 0, 0, 0, false
	}
	if res.CountPlanes == 0 {
		log.Printf("drm: no planes reported")
		return 0, 0, 0, false
	}

	planes := make([]uint32, res.CountPlanes)
	res.PlaneIDPtr = uint64(uintptr(unsafe.Pointer(&planes[0])))
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlaneRes, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil {
		log.Printf("drm: get plane resource ids failed: %v", err)
		return 0, 0, 0, false
	}

	for _, planeID := range planes {
		plane := drmModeGetPlane{PlaneID: planeID}
		if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlane, unsafe.Sizeof(plane)), unsafe.Pointer(&plane)); err != nil {
			log.Printf("drm: get plane %d failed: %v", planeID, err)
			continue
		}
		// Complete the query with a valid format array. Some vendor drivers are
		// stricter than mainline DRM about the pointer being non-NULL whenever
		// CountFormatTypes is non-zero.
		formats := make([]uint32, plane.CountFormatTypes)
		if len(formats) > 0 {
			plane.FormatTypePtr = uint64(uintptr(unsafe.Pointer(&formats[0])))
			if err := drmIoctl(fd, drmIOWR(drmIoctlGetPlane, unsafe.Sizeof(plane)), unsafe.Pointer(&plane)); err != nil {
				log.Printf("drm: get plane %d formats failed: %v", planeID, err)
				continue
			}
		}
		log.Printf("drm: plane %d crtc=%d fb=%d possible_crtcs=0x%x formats=%d", planeID, plane.CRTCID, plane.FBID, plane.PossibleCRTCS, plane.CountFormatTypes)
		if plane.CRTCID != 0 && plane.FBID != 0 {
			return planeID, plane.CRTCID, plane.FBID, true
		}
	}
	return 0, 0, 0, false
}

func drmFindEmbeddedPanel(fd uintptr, connectorIDs, crtcIDs []uint32) (uint32, drmModeModeInfo, uint32, bool) {
	if len(connectorIDs) == 0 || len(crtcIDs) == 0 {
		return 0, drmModeModeInfo{}, 0, false
	}

	type candidate struct {
		connectorID uint32
		mode        drmModeModeInfo
		crtcID      uint32
		score       int
	}
	var best *candidate

	for _, connectorID := range connectorIDs {
		conn, encoderIDs, modes, err := drmGetConnector(fd, connectorID)
		if err != nil || len(modes) == 0 {
			if err != nil {
				log.Printf("drm: embedded-panel connector %d query failed: %v", connectorID, err)
			}
			continue
		}

		mode := modes[0]
		for _, m := range modes {
			if m.Type&drmModeTypePreferred != 0 {
				mode = m
				break
			}
		}

		base := 0
		if conn.ConnectorType == drmModeConnectorDPI {
			base += 1000
		}
		if conn.Connection == drmModeConnected {
			base += 100
		}
		if conn.EncoderID != 0 {
			base += 50
		}
		log.Printf("drm: embedded-panel candidate connector=%d type=%d connection=%d encoder=%d modes=%d mode=%s", connectorID, conn.ConnectorType, conn.Connection, conn.EncoderID, len(modes), drmModeName(mode))

		// First try an encoder's current CRTC or possible CRTCs. This is only
		// a preference; failure to find one must not reject a fixed DPI panel.
		preferredCRTCS := make(map[uint32]int)
		encoderSet := append([]uint32(nil), encoderIDs...)
		if conn.EncoderID != 0 {
			seen := false
			for _, id := range encoderSet {
				if id == conn.EncoderID {
					seen = true
					break
				}
			}
			if !seen {
				encoderSet = append(encoderSet, conn.EncoderID)
			}
		}
		for _, encoderID := range encoderSet {
			enc := drmModeGetEncoder{EncoderID: encoderID}
			if err := drmIoctl(fd, drmIOWR(drmIoctlGetEncoder, unsafe.Sizeof(enc)), unsafe.Pointer(&enc)); err != nil {
				continue
			}
			for index, crtcID := range crtcIDs {
				if crtcID == enc.CRTCID && enc.CRTCID != 0 {
					preferredCRTCS[crtcID] = 300
				}
				if index < 32 && enc.PossibleCRTCS&(1<<uint(index)) != 0 {
					if preferredCRTCS[crtcID] < 200 {
						preferredCRTCS[crtcID] = 200
					}
				}
			}
		}

		// A current CRTC whose reported mode matches the connector mode is an
		// especially strong signal for embedded panels, even when FBID/active
		// are reported as zero by the vendor driver.
		for _, crtcID := range crtcIDs {
			crtc := drmModeCRTC{CRTCID: crtcID}
			if err := drmIoctl(fd, drmIOWR(drmIoctlGetCRTC, unsafe.Sizeof(crtc)), unsafe.Pointer(&crtc)); err == nil {
				if crtc.ModeValid != 0 && crtc.Mode.HDisplay == mode.HDisplay && crtc.Mode.VDisplay == mode.VDisplay {
					if preferredCRTCS[crtcID] < 500 {
						preferredCRTCS[crtcID] = 500
					}
				}
				log.Printf("drm: embedded-panel CRTC %d fb=%d mode_valid=%d mode=%s preference=%d", crtcID, crtc.FBID, crtc.ModeValid, drmModeName(crtc.Mode), preferredCRTCS[crtcID])
			}
		}

		// If this looks like the fixed DPI connector exposed by RV1106, a
		// single available CRTC is unambiguous. With multiple CRTCs, prefer the
		// one with the best encoder/mode match instead.
		for _, crtcID := range crtcIDs {
			score := base + preferredCRTCS[crtcID]
			if len(crtcIDs) == 1 {
				score += 100
			}
			if conn.ConnectorType == drmModeConnectorDPI {
				score += 100
			}
			if best == nil || score > best.score {
				c := candidate{connectorID: connectorID, mode: mode, crtcID: crtcID, score: score}
				best = &c
			}
		}
	}

	if best == nil {
		return 0, drmModeModeInfo{}, 0, false
	}
	log.Printf("drm: embedded-panel selected connector=%d crtc=%d mode=%s score=%d", best.connectorID, best.crtcID, drmModeName(best.mode), best.score)
	return best.connectorID, best.mode, best.crtcID, true
}

func drmFindUsableConnectorMode(fd uintptr, connectorIDs []uint32) (uint32, drmModeModeInfo) {
	type candidate struct {
		id    uint32
		mode  drmModeModeInfo
		score int
	}
	var best *candidate
	for _, connectorID := range connectorIDs {
		conn, _, modes, err := drmGetConnector(fd, connectorID)
		if err != nil {
			log.Printf("drm: get connector %d failed while finding mode: %v", connectorID, err)
			continue
		}
		if len(modes) == 0 {
			continue
		}
		mode := modes[0]
		score := 0
		if conn.Connection == drmModeConnected {
			score += 100
		}
		for _, m := range modes {
			if m.Type&drmModeTypePreferred != 0 {
				mode = m
				score += 10
				break
			}
		}
		log.Printf("drm: connector %d connection=%d modes=%d encoder=%d candidate_mode=%s", connectorID, conn.Connection, len(modes), conn.EncoderID, drmModeName(mode))
		if best == nil || score > best.score {
			best = &candidate{id: connectorID, mode: mode, score: score}
		}
	}
	if best == nil {
		return 0, drmModeModeInfo{}
	}
	return best.id, best.mode
}

func drmFindConnectorForCRTC(fd uintptr, connectorIDs []uint32, targetCRTC uint32) uint32 {
	for _, connectorID := range connectorIDs {
		conn, encoderIDs, _, err := drmGetConnector(fd, connectorID)
		if err != nil {
			continue
		}

		encoderSet := append([]uint32(nil), encoderIDs...)
		if conn.EncoderID != 0 {
			found := false
			for _, id := range encoderSet {
				if id == conn.EncoderID {
					found = true
					break
				}
			}
			if !found {
				encoderSet = append(encoderSet, conn.EncoderID)
			}
		}

		for _, encoderID := range encoderSet {
			enc := drmModeGetEncoder{EncoderID: encoderID}
			if err := drmIoctl(fd, drmIOWR(drmIoctlGetEncoder, unsafe.Sizeof(enc)), unsafe.Pointer(&enc)); err != nil {
				continue
			}
			if enc.CRTCID == targetCRTC {
				return connectorID
			}
		}
	}
	return 0
}

func drmFindActiveCRTC(fd uintptr, crtcIDs []uint32) (uint32, drmModeCRTC, bool) {
	for _, crtcID := range crtcIDs {
		crtc := drmModeCRTC{CRTCID: crtcID}
		if err := drmIoctl(fd, drmIOWR(drmIoctlGetCRTC, unsafe.Sizeof(crtc)), unsafe.Pointer(&crtc)); err != nil {
			log.Printf("drm: get CRTC %d failed: %v", crtcID, err)
			continue
		}
		log.Printf("drm: CRTC %d fb=%d mode_valid=%d mode=%s %dx%d", crtcID, crtc.FBID, crtc.ModeValid, drmModeName(crtc.Mode), crtc.Mode.HDisplay, crtc.Mode.VDisplay)
		if crtc.ModeValid != 0 && crtc.FBID != 0 && crtc.Mode.HDisplay != 0 && crtc.Mode.VDisplay != 0 {
			return crtcID, crtc, true
		}
	}
	return 0, drmModeCRTC{}, false
}

func drmGetResources(fd uintptr) (drmModeCardRes, []uint32, []uint32, []uint32, error) {
	var res drmModeCardRes
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetResources, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil {
		return res, nil, nil, nil, fmt.Errorf("drm get resources: %w", err)
	}
	if res.CountConnectors == 0 || res.CountCRTCs == 0 {
		return res, nil, nil, nil, errors.New("drm: no connector or CRTC")
	}

	connectors := make([]uint32, res.CountConnectors)
	encoders := make([]uint32, res.CountEncoders)
	crtcs := make([]uint32, res.CountCRTCs)
	res.ConnectorIDPtr = uint64(uintptr(unsafe.Pointer(&connectors[0])))
	res.CRTCIDPtr = uint64(uintptr(unsafe.Pointer(&crtcs[0])))
	if len(encoders) > 0 {
		res.EncoderIDPtr = uint64(uintptr(unsafe.Pointer(&encoders[0])))
	}
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetResources, unsafe.Sizeof(res)), unsafe.Pointer(&res)); err != nil {
		return res, nil, nil, nil, fmt.Errorf("drm get resource ids: %w", err)
	}
	return res, connectors, encoders, crtcs, nil
}

func drmSelectDisplay(fd uintptr, connectorIDs, encoderIDs, crtcIDs []uint32) (uint32, drmModeModeInfo, uint32, error) {
	// Embedded panels (DSI/eDP/internal LCD) do not always report
	// DRM_MODE_CONNECTED. What matters for KMS is that the connector has a
	// mode and an encoder route to a usable CRTC. Prefer an already-active
	// encoder/CRTC, then fall back to any valid encoder/CRTC combination.
	//
	// Keep the resource encoder list as a last-resort source because some
	// drivers expose the connector's current encoder only through encoder_id.
	allEncoders := append([]uint32(nil), encoderIDs...)
	seenEncoder := make(map[uint32]struct{}, len(allEncoders))
	for _, id := range allEncoders {
		seenEncoder[id] = struct{}{}
	}

	type candidate struct {
		connectorID uint32
		mode        drmModeModeInfo
		crtcID      uint32
		score       int
	}

	var best *candidate
	for _, connectorID := range connectorIDs {
		conn, encoders, modes, err := drmGetConnector(fd, connectorID)
		if err != nil {
			log.Printf("drm: get connector %d failed: %v", connectorID, err)
			continue
		}
		if len(modes) == 0 {
			log.Printf("drm: connector %d has no modes (connection=%d encoder=%d)", connectorID, conn.Connection, conn.EncoderID)
			continue
		}

		mode := modes[0]
		for _, candidateMode := range modes {
			if candidateMode.Type&drmModeTypePreferred != 0 {
				mode = candidateMode
				break
			}
		}

		// The current encoder is not guaranteed to be repeated in the
		// connector's encoder array on every embedded-panel driver.
		if conn.EncoderID != 0 {
			if _, ok := seenEncoder[conn.EncoderID]; !ok {
				encoders = append(encoders, conn.EncoderID)
				seenEncoder[conn.EncoderID] = struct{}{}
			}
		}

		// A few simple embedded-panel drivers don't populate the connector's
		// encoder list even though the resource list contains the encoder that
		// drives the only connector. Only use that fallback when the DRM device
		// exposes a single connector, so we don't guess across outputs.
		if len(encoders) == 0 && conn.EncoderID == 0 && len(connectorIDs) == 1 && len(encoderIDs) == 1 {
			encoders = append(encoders, encoderIDs[0])
		}

		log.Printf("drm: connector %d connection=%d modes=%d current_encoder=%d encoders=%v", connectorID, conn.Connection, len(modes), conn.EncoderID, encoders)

		for _, encoderID := range encoders {
			enc := drmModeGetEncoder{EncoderID: encoderID}
			if err := drmIoctl(fd, drmIOWR(drmIoctlGetEncoder, unsafe.Sizeof(enc)), unsafe.Pointer(&enc)); err != nil {
				log.Printf("drm: get encoder %d failed: %v", encoderID, err)
				continue
			}

			// Prefer the encoder's current CRTC. This is especially important
			// for fixed panels where connector->encoder routing is static but
			// the connector status may be UNKNOWN.
			if enc.CRTCID != 0 {
				for _, crtcID := range crtcIDs {
					if crtcID != enc.CRTCID {
						continue
					}
					score := 100
					if conn.Connection == drmModeConnected {
						score += 20
					}
					if conn.EncoderID == encoderID {
						score += 10
					}
					if best == nil || score > best.score {
						c := candidate{connectorID: connectorID, mode: mode, crtcID: crtcID, score: score}
						best = &c
					}
				}
			}

			for index, crtcID := range crtcIDs {
				if index >= 32 || enc.PossibleCRTCS&(1<<uint(index)) == 0 {
					continue
				}
				score := 50
				if conn.Connection == drmModeConnected {
					score += 20
				}
				if conn.EncoderID == encoderID {
					score += 10
				}
				if best == nil || score > best.score {
					c := candidate{connectorID: connectorID, mode: mode, crtcID: crtcID, score: score}
					best = &c
				}
			}
		}
	}

	if best != nil {
		log.Printf("drm: selected connector=%d crtc=%d mode=%s score=%d", best.connectorID, best.crtcID, drmModeName(best.mode), best.score)
		return best.connectorID, best.mode, best.crtcID, nil
	}
	return 0, drmModeModeInfo{}, 0, errors.New("drm: no connector with usable encoder/CRTC")
}

func drmModeName(mode drmModeModeInfo) string {
	n := 0
	for n < len(mode.Name) && mode.Name[n] != 0 {
		n++
	}
	return string(mode.Name[:n])
}

func drmGetConnector(fd uintptr, connectorID uint32) (drmModeGetConnector, []uint32, []drmModeModeInfo, error) {
	conn := drmModeGetConnector{ConnectorID: connectorID}
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetConnector, unsafe.Sizeof(conn)), unsafe.Pointer(&conn)); err != nil {
		return conn, nil, nil, err
	}

	// The first GETCONNECTOR is a size query, but the kernel may still expect
	// valid userspace arrays for properties on the second call. Leaving
	// props_ptr/prop_values_ptr as NULL can make vendor DRM drivers return
	// EFAULT ("bad address") even when modes and encoders are available.
	encoders := make([]uint32, conn.CountEncoders)
	modes := make([]drmModeModeInfo, conn.CountModes)
	props := make([]uint32, conn.CountProps)
	propValues := make([]uint32, conn.CountProps)
	if len(encoders) > 0 {
		conn.EncodersPtr = uint64(uintptr(unsafe.Pointer(&encoders[0])))
	}
	if len(modes) > 0 {
		conn.ModesPtr = uint64(uintptr(unsafe.Pointer(&modes[0])))
	}
	if len(props) > 0 {
		conn.PropsPtr = uint64(uintptr(unsafe.Pointer(&props[0])))
		conn.PropValuesPtr = uint64(uintptr(unsafe.Pointer(&propValues[0])))
	}
	if err := drmIoctl(fd, drmIOWR(drmIoctlGetConnector, unsafe.Sizeof(conn)), unsafe.Pointer(&conn)); err != nil {
		return conn, nil, nil, err
	}
	return conn, encoders, modes, nil
}

func drmCreateBuffer(fd uintptr, width, height uint32, format drmFormatChoice, buf *drmFBBuffer) error {
	create := drmModeCreateDumb{Width: width, Height: height, BPP: format.bpp}
	if err := drmIoctl(fd, drmIOWR(drmIoctlCreateDumb, unsafe.Sizeof(create)), unsafe.Pointer(&create)); err != nil {
		return fmt.Errorf("drm create dumb buffer (%s): %w", format.name, err)
	}
	buf.handle = create.Handle
	buf.pitch = create.Pitch
	buf.size = create.Size

	mapReq := drmModeMapDumb{Handle: create.Handle}
	if err := drmIoctl(fd, drmIOWR(drmIoctlMapDumb, unsafe.Sizeof(mapReq)), unsafe.Pointer(&mapReq)); err != nil {
		_ = drmIoctl(fd, drmIOWR(drmIoctlDestroyDumb, unsafe.Sizeof(create.Handle)), unsafe.Pointer(&create.Handle))
		buf.handle = 0
		return fmt.Errorf("drm map dumb buffer (%s): %w", format.name, err)
	}

	mapped, err := syscall.Mmap(int(fd), int64(mapReq.Offset), int(create.Size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		_ = drmIoctl(fd, drmIOWR(drmIoctlDestroyDumb, unsafe.Sizeof(create.Handle)), unsafe.Pointer(&create.Handle))
		buf.handle = 0
		return fmt.Errorf("drm mmap dumb buffer (%s): %w", format.name, err)
	}
	buf.mapped = mapped

	fb := drmModeFB2{Width: width, Height: height, PixelFormat: format.fourcc}
	fb.Handles[0] = create.Handle
	fb.Pitches[0] = create.Pitch
	if err := drmIoctl(fd, drmIOWR(0xB8, unsafe.Sizeof(fb)), unsafe.Pointer(&fb)); err != nil {
		_ = syscall.Munmap(buf.mapped)
		buf.mapped = nil
		_ = drmIoctl(fd, drmIOWR(drmIoctlDestroyDumb, unsafe.Sizeof(create.Handle)), unsafe.Pointer(&create.Handle))
		buf.handle = 0
		return fmt.Errorf("drm add framebuffer (%s): %w", format.name, err)
	}
	buf.fbID = fb.FBID
	return nil
}

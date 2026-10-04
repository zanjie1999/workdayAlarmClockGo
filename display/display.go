package display

import (
	"errors"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
)

const DevicePath = "/dev/fb0"

var ErrUnsupported = errors.New("framebuffer display is only supported on Linux")

type Bitfield struct{ Offset, Length, MsbRight uint32 }

type Info struct {
	Device        string `json:"device"`
	Width         uint32 `json:"width"`
	Height        uint32 `json:"height"`
	WidthVirtual  uint32 `json:"width_virtual"`
	HeightVirtual uint32 `json:"height_virtual"`
	BitsPerPixel  uint32 `json:"bits_per_pixel"`
	Format        string `json:"format"`
	Stride        uint32 `json:"stride"`
	FrameSize     uint64 `json:"frame_size"`
	MemorySize    uint32 `json:"memory_size"`
	YPanStep      uint16 `json:"y_pan_step"`
	DoubleBuffer  bool   `json:"double_buffer"`
	RedOffset     uint32 `json:"red_offset"`
	RedLength     uint32 `json:"red_length"`
	GreenOffset   uint32 `json:"green_offset"`
	GreenLength   uint32 `json:"green_length"`
	BlueOffset    uint32 `json:"blue_offset"`
	BlueLength    uint32 `json:"blue_length"`
	AlphaOffset   uint32 `json:"alpha_offset"`
	AlphaLength   uint32 `json:"alpha_length"`
}

type StreamStats struct {
	Frames    uint64 `json:"frames"`
	JPEGBytes uint64 `json:"jpeg_bytes"`
}

func applyResolutionOverride(info Info) (Info, error) {
	widthValue, widthSet := os.LookupEnv("FB_WIDTH")
	heightValue, heightSet := os.LookupEnv("FB_HEIGHT")
	if !widthSet && !heightSet {
		return info, nil
	}
	if !widthSet || !heightSet || strings.TrimSpace(widthValue) == "" || strings.TrimSpace(heightValue) == "" {
		return Info{}, errors.New("FB_WIDTH and FB_HEIGHT must both be set")
	}

	width, err := strconv.ParseUint(strings.TrimSpace(widthValue), 10, 32)
	if err != nil || width == 0 {
		return Info{}, fmt.Errorf("invalid FB_WIDTH %q: must be a positive integer", widthValue)
	}
	height, err := strconv.ParseUint(strings.TrimSpace(heightValue), 10, 32)
	if err != nil || height == 0 {
		return Info{}, fmt.Errorf("invalid FB_HEIGHT %q: must be a positive integer", heightValue)
	}

	info.Width = uint32(width)
	info.Height = uint32(height)
	return info, nil
}

func centerImage(img image.Image, targetWidth, targetHeight int) (image.Image, int, int, error) {
	if img == nil || targetWidth <= 0 || targetHeight <= 0 {
		return nil, 0, 0, errors.New("invalid image dimensions")
	}
	subImager, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, 0, 0, errors.New("image does not support centered cropping")
	}

	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, 0, 0, errors.New("invalid image dimensions")
	}
	width := minDimension(bounds.Dx(), targetWidth)
	height := minDimension(bounds.Dy(), targetHeight)
	cropX := bounds.Min.X + (bounds.Dx()-width)/2
	cropY := bounds.Min.Y + (bounds.Dy()-height)/2
	cropped := subImager.SubImage(image.Rect(cropX, cropY, cropX+width, cropY+height))
	return cropped, (targetWidth - width) / 2, (targetHeight - height) / 2, nil
}

func minDimension(a, b int) int {
	if a < b {
		return a
	}
	return b
}

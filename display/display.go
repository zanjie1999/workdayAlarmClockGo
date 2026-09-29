package display

import "errors"

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

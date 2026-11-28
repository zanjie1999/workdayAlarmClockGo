//go:build linux && cgo && alsa && !app

package xiaozhi

/*
#cgo linux LDFLAGS: -lopus -lm

#include <opus/opus.h>
#include <stdlib.h>

// opus_encoder_ctl 是变参函数，cgo 无法直接调用（会报 unexpected type: ...），
// 在 C 侧包一层再给 Go 用。
static void wx_opus_set_bitrate(OpusEncoder *enc, int bitrate) {
	opus_encoder_ctl(enc, OPUS_SET_BITRATE(bitrate));
}
static void wx_opus_set_complexity(OpusEncoder *enc, int complexity) {
	opus_encoder_ctl(enc, OPUS_SET_COMPLEXITY(complexity));
}
static void wx_opus_set_vbr(OpusEncoder *enc, int vbr) {
	opus_encoder_ctl(enc, OPUS_SET_VBR(vbr));
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// 一包 opus 的上限：协议给的是 1275 字节，留点余量
const maxOpusPacket = 1500

// opusEncoder 上行编码器：16k 单声道，60ms 一帧。
type opusEncoder struct {
	enc *C.OpusEncoder
}

func newOpusEncoder(sampleRate, channels, bitrate, complexity int) (*opusEncoder, error) {
	var errCode C.int
	enc := C.opus_encoder_create(C.opus_int32(sampleRate), C.int(channels), C.OPUS_APPLICATION_VOIP, &errCode)
	if errCode != C.OPUS_OK || enc == nil {
		return nil, fmt.Errorf("opus 编码器创建失败：%d", int(errCode))
	}
	C.wx_opus_set_bitrate(enc, C.int(bitrate))
	C.wx_opus_set_complexity(enc, C.int(complexity))
	C.wx_opus_set_vbr(enc, C.int(1))
	return &opusEncoder{enc: enc}, nil
}

// Encode 把一个满帧（FrameSamples 个采样）编成 opus 包。
func (e *opusEncoder) Encode(pcm []int16) ([]byte, error) {
	if e == nil || e.enc == nil {
		return nil, errors.New("编码器已关闭")
	}
	if len(pcm) == 0 {
		return nil, nil
	}
	out := make([]byte, maxOpusPacket)
	n := C.opus_encode(
		e.enc,
		(*C.opus_int16)(unsafe.Pointer(&pcm[0])),
		C.int(len(pcm)),
		(*C.uchar)(unsafe.Pointer(&out[0])),
		C.opus_int32(len(out)),
	)
	if n < 0 {
		return nil, fmt.Errorf("opus 编码失败：%d", int(n))
	}
	return out[:int(n)], nil
}

func (e *opusEncoder) Close() {
	if e == nil || e.enc == nil {
		return
	}
	C.opus_encoder_destroy(e.enc)
	e.enc = nil
}

// opusDecoder 下行解码器，采样率由服务端握手时下发。
type opusDecoder struct {
	dec *C.OpusDecoder
	// maxFrame opus 单包最长 120ms，按最大可能分配，避免每包都新建数组
	maxFrame int
}

func newOpusDecoder(sampleRate int) (*opusDecoder, error) {
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	var errCode C.int
	dec := C.opus_decoder_create(C.opus_int32(sampleRate), 1, &errCode)
	if errCode != C.OPUS_OK || dec == nil {
		return nil, fmt.Errorf("opus 解码器创建失败：%d", int(errCode))
	}
	return &opusDecoder{dec: dec, maxFrame: sampleRate / 1000 * 120}, nil
}

func (d *opusDecoder) Decode(pkt []byte) ([]int16, error) {
	if d == nil || d.dec == nil {
		return nil, errors.New("解码器已关闭")
	}
	if len(pkt) == 0 {
		return nil, nil
	}
	out := make([]int16, d.maxFrame)
	n := C.opus_decode(
		d.dec,
		(*C.uchar)(unsafe.Pointer(&pkt[0])),
		C.opus_int32(len(pkt)),
		(*C.opus_int16)(unsafe.Pointer(&out[0])),
		C.int(d.maxFrame),
		0,
	)
	if n < 0 {
		return nil, fmt.Errorf("opus 解码失败：%d", int(n))
	}
	return out[:int(n)], nil
}

func (d *opusDecoder) Close() {
	if d == nil || d.dec == nil {
		return
	}
	C.opus_decoder_destroy(d.dec)
	d.dec = nil
}

/*
 * mp3dur.go -- 从 MP3 头部算总时长
 *
 * MP3 没有"全局时长"字段（不像 MP4 有 moov），它只是一串帧。时长只有三条路：
 *   1. 扫完所有帧（go-mp3 的 Decoder.Length() 走这条，但要求源能 Seek，
 *      网络流不是 Seeker，所以直接返回 -1）；
 *   2. 读第一帧里的 Xing/Info（或 VBRI）头，里面记着总帧数 → 帧数 × 采样/帧 ÷ 采样率；
 *   3. CBR 时用「音频字节数 ÷ 码率」估算。
 *
 * 这里按 2 → 3 的顺序做，都不成立就返回 0（外壳显示不确定态，不瞎猜）。
 * 调用方用 bufio.Peek 拿前 64KB，不会消费流、也不会多发一次请求。
 */
package player

import (
	"bufio"
	"encoding/binary"
	"os"
)

var (
	// Layer III 码率表（kbps），下标是帧头里的 bitrate index
	mp3KbpsV1 = [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3KbpsV2 = [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}

	mp3RateV1  = [...]int{44100, 48000, 32000, 0}
	mp3RateV2  = [...]int{22050, 24000, 16000, 0}
	mp3RateV25 = [...]int{11025, 12000, 8000, 0}
)

// probeMP3Duration 返回毫秒，0 = 不知道
func probeMP3Duration(head []byte, totalSize int64) int64 {
	if len(head) < 4 {
		return 0
	}

	off := 0
	// ID3v2：10 字节头 + syncsafe 长度
	if len(head) >= 10 && head[0] == 'I' && head[1] == 'D' && head[2] == '3' {
		size := int(head[6]&0x7F)<<21 | int(head[7]&0x7F)<<14 | int(head[8]&0x7F)<<7 | int(head[9]&0x7F)
		off = 10 + size
		if head[5]&0x10 != 0 { // footer
			off += 10
		}
	}

	// 找帧同步。ID3 长度不可信时（有些文件填 0）最多再往前扫 4KB
	limit := off + 4096
	if limit > len(head) {
		limit = len(head)
	}
	for off+4 <= limit && !(head[off] == 0xFF && head[off+1]&0xE0 == 0xE0) {
		off++
	}
	if off+4 > len(head) {
		return 0
	}

	h := head[off:]
	verBits := (h[1] >> 3) & 0x03 // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
	layer := (h[1] >> 1) & 0x03   // 1 = Layer III
	if layer != 1 {
		return 0
	}
	brIdx := int(h[2] >> 4)
	srIdx := int((h[2] >> 2) & 0x03)
	if brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return 0
	}

	var rate, kbps, spf int
	switch verBits {
	case 3:
		rate, kbps, spf = mp3RateV1[srIdx], mp3KbpsV1[brIdx], 1152
	case 2:
		rate, kbps, spf = mp3RateV2[srIdx], mp3KbpsV2[brIdx], 576
	case 0:
		rate, kbps, spf = mp3RateV25[srIdx], mp3KbpsV2[brIdx], 576
	default:
		return 0
	}
	if rate == 0 || kbps == 0 {
		return 0
	}

	mono := (h[3] >> 6) == 3

	// Xing/Info 紧跟在帧头 + side info 之后
	xingOff := 21
	if verBits == 3 {
		if !mono {
			xingOff = 36
		}
	} else if !mono {
		xingOff = 21
	} else {
		xingOff = 13
	}
	if p := off + xingOff; p+12 <= len(head) {
		tag := string(head[p : p+4])
		if tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(head[p+4:])
			if flags&0x01 != 0 {
				if frames := binary.BigEndian.Uint32(head[p+8:]); frames > 0 {
					return int64(frames) * int64(spf) * 1000 / int64(rate)
				}
			}
		}
	}

	// VBRI（Fraunhofer 编码器）固定在帧头后 32 字节：VBRI(4) ver(2) delay(2) quality(2) bytes(4) frames(4)
	if p := off + 36; p+18 <= len(head) && string(head[p:p+4]) == "VBRI" {
		if frames := binary.BigEndian.Uint32(head[p+14:]); frames > 0 {
			return int64(frames) * int64(spf) * 1000 / int64(rate)
		}
	}

	// 兜底：CBR 估算。ms = 字节数 × 8 ÷ kbps
	if totalSize > int64(off) {
		return (totalSize - int64(off)) * 8 / int64(kbps)
	}
	return 0
}

// streamSize 尽量拿到数据总字节数（本地文件 / 带 Content-Length 的流）
func streamSize(v interface{}) int64 {
	switch s := v.(type) {
	case interface{ TotalSize() int64 }:
		return s.TotalSize()
	case *os.File:
		if st, err := s.Stat(); err == nil {
			return st.Size()
		}
	}
	return 0
}

// probeStream 在解码前探一次时长；返回的 reader 可以原样交给 mp3.NewDecoder
// Peek 不会消费数据，EOF 时只是拿到的少，能拿多少解析多少
func probeStream(r *bufio.Reader, size int64) int64 {
	head, _ := r.Peek(64 * 1024)
	if len(head) == 0 {
		return 0
	}
	return probeMP3Duration(head, size)
}

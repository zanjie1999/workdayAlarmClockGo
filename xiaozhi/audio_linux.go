//go:build linux && cgo && alsa && !app

package xiaozhi

/*
#cgo linux LDFLAGS: -lasound

#include <alsa/asoundlib.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>

// wx_pcm 一收一放共用的句柄。stream: 0=播放 1=采集。
typedef struct {
	snd_pcm_t *pcm;
	unsigned int rate;
	unsigned int channels;
	unsigned int frame_bytes;
} wx_pcm;

static wx_pcm *wx_pcm_open(const char *dev, int stream, unsigned int rate, int channels) {
	wx_pcm *p = (wx_pcm *)calloc(1, sizeof(wx_pcm));
	if (p == NULL) {
		return NULL;
	}
	snd_pcm_stream_t dir = stream == 0 ? SND_PCM_STREAM_PLAYBACK : SND_PCM_STREAM_CAPTURE;
	if (snd_pcm_open(&p->pcm, dev, dir, 0) < 0) {
		free(p);
		return NULL;
	}

	snd_pcm_hw_params_t *hw;
	snd_pcm_hw_params_alloca(&hw);
	if (snd_pcm_hw_params_any(p->pcm, hw) < 0 ||
	    snd_pcm_hw_params_set_access(p->pcm, hw, SND_PCM_ACCESS_RW_INTERLEAVED) < 0 ||
	    snd_pcm_hw_params_set_format(p->pcm, hw, SND_PCM_FORMAT_S16_LE) < 0 ||
	    snd_pcm_hw_params_set_channels(p->pcm, hw, (unsigned int)channels) < 0) {
		snd_pcm_close(p->pcm);
		free(p);
		return NULL;
	}

	unsigned int actual = rate;
	int dir2 = 0;
	if (snd_pcm_hw_params_set_rate_near(p->pcm, hw, &actual, &dir2) < 0) {
		snd_pcm_close(p->pcm);
		free(p);
		return NULL;
	}

	// 20ms 周期、80ms 缓冲：够稳又不至于延迟太大
	unsigned int period = 20000;
	dir2 = 0;
	snd_pcm_hw_params_set_period_time_near(p->pcm, hw, &period, &dir2);
	unsigned int buffer = 80000;
	dir2 = 0;
	snd_pcm_hw_params_set_buffer_time_near(p->pcm, hw, &buffer, &dir2);

	if (snd_pcm_hw_params(p->pcm, hw) < 0) {
		snd_pcm_close(p->pcm);
		free(p);
		return NULL;
	}

	p->rate = actual;
	p->channels = (unsigned int)channels;
	p->frame_bytes = (unsigned int)channels * 2;
	return p;
}

static void wx_pcm_close(wx_pcm *p) {
	if (p == NULL) {
		return;
	}
	if (p->pcm != NULL) {
		snd_pcm_drain(p->pcm);
		snd_pcm_close(p->pcm);
	}
	free(p);
}

// wx_pcm_write 写满要求的帧数，遇到欠载自动恢复。
static long wx_pcm_write(wx_pcm *p, void *data, unsigned long frames) {
	unsigned long done = 0;
	char *ptr = (char *)data;
	while (done < frames) {
		snd_pcm_sframes_t n = snd_pcm_writei(p->pcm, ptr + done * p->frame_bytes, frames - done);
		if (n < 0) {
			n = snd_pcm_recover(p->pcm, (int)n, 1);
			if (n < 0) {
				return n;
			}
			continue;
		}
		if (n == 0) {
			continue;
		}
		done += (unsigned long)n;
	}
	return (long)done;
}

// wx_pcm_read 尽量读满 frames 帧，返回实际读到的帧数（0 表示暂时没有）。
static long wx_pcm_read(wx_pcm *p, void *data, unsigned long frames) {
	snd_pcm_sframes_t n = snd_pcm_readi(p->pcm, data, frames);
	if (n < 0) {
		n = snd_pcm_recover(p->pcm, (int)n, 1);
		if (n < 0) {
			return n;
		}
		return 0;
	}
	return (long)n;
}

static const char *wx_pcm_error(int err) {
	return snd_strerror(err);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"workdayAlarmClock/xiaozhi/core"
)

// alsaPCM 一个 ALSA 句柄，只在本文件里用。
type alsaPCM struct {
	handle *C.wx_pcm
	rate   int
	ch     int
}

func openALSA(device string, capture bool, rate, channels int) (*alsaPCM, error) {
	cdev := C.CString(device)
	defer C.free(unsafe.Pointer(cdev))
	stream := C.int(0)
	if capture {
		stream = C.int(1)
	}
	h := C.wx_pcm_open(cdev, stream, C.uint(rate), C.int(channels))
	if h == nil {
		kind := "播放"
		if capture {
			kind = "采集"
		}
		return nil, fmt.Errorf("打不开%s设备 %s（%d 声道 / %d Hz）", kind, device, channels, rate)
	}
	return &alsaPCM{handle: h, rate: int(h.rate), ch: channels}, nil
}

func (p *alsaPCM) write(samples []int16) error {
	if p == nil || p.handle == nil || len(samples) == 0 {
		return nil
	}
	frames := C.ulong(len(samples) / p.ch)
	n := C.wx_pcm_write(p.handle, unsafe.Pointer(&samples[0]), frames)
	if n < 0 {
		return errors.New(C.GoString(C.wx_pcm_error(C.int(n))))
	}
	return nil
}

func (p *alsaPCM) read(buf []int16) (int, error) {
	if p == nil || p.handle == nil || len(buf) == 0 {
		return 0, nil
	}
	frames := C.ulong(len(buf) / p.ch)
	n := C.wx_pcm_read(p.handle, unsafe.Pointer(&buf[0]), frames)
	if n < 0 {
		return 0, errors.New(C.GoString(C.wx_pcm_error(C.int(n))))
	}
	return int(n) * p.ch, nil
}

func (p *alsaPCM) close() {
	if p == nil || p.handle == nil {
		return
	}
	C.wx_pcm_close(p.handle)
	p.handle = nil
}

// playbackSink TTS 播放。单独一个 goroutine 写声卡，避免把会话线程堵住。
type playbackSink struct {
	device string
	log    func(string)

	mu   sync.Mutex
	rate int
	pcm  *alsaPCM
	ch   chan []int16

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func newPlaybackSink(device string, log func(string)) *playbackSink {
	if log == nil {
		log = func(string) {}
	}
	s := &playbackSink{
		device: device,
		log:    log,
		ch:     make(chan []int16, 64),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go s.loop()
	return s
}

// Play 入队。队列满了丢最老的，宁可漏一句也不要卡住会话线程。
func (s *playbackSink) Play(pcm []int16, rate int) error {
	if len(pcm) == 0 {
		return nil
	}
	s.mu.Lock()
	s.rate = rate
	s.mu.Unlock()

	select {
	case s.ch <- pcm:
		return nil
	default:
		select {
		case <-s.ch:
			s.log("播放队列已满，丢弃一包")
		default:
		}
		select {
		case s.ch <- pcm:
		default:
		}
		return nil
	}
}

func (s *playbackSink) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case pcm := <-s.ch:
			s.mu.Lock()
			rate := s.rate
			pcmDev := s.pcm
			if pcmDev == nil || pcmDev.rate != rate {
				if pcmDev != nil {
					pcmDev.close()
				}
				opened, err := openALSA(s.device, false, rate, 1)
				if err != nil {
					s.log(fmt.Sprintf("打开播放设备失败：%v", err))
					pcmDev = nil
				} else {
					pcmDev = opened
				}
				s.pcm = pcmDev
			}
			s.mu.Unlock()

			if pcmDev == nil {
				continue
			}
			if err := pcmDev.write(pcm); err != nil {
				s.log(fmt.Sprintf("播放出错，重开设备：%v", err))
				s.mu.Lock()
				pcmDev.close()
				s.pcm = nil
				s.mu.Unlock()
			}
		}
	}
}

// Flush 丢掉还没播的内容。
func (s *playbackSink) Flush() {
	for {
		select {
		case <-s.ch:
		default:
			return
		}
	}
}

// Pending 队列里还有多少包没播。
func (s *playbackSink) Pending() int { return len(s.ch) }

func (s *playbackSink) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	s.mu.Lock()
	s.pcm.close()
	s.pcm = nil
	s.mu.Unlock()
}

// captureSource 麦克风采集。把硬件给的多声道数据混成单声道，再切成一帧帧交给上层。
type captureSource struct {
	device  string
	backend string
	ch      int
	rate    int
	pcm     *alsaPCM
	log     func(string)

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// openCapture 按 5 → 1 → 2 声道依次试。
// 这台音箱的 DMIC 驱动只暴露 5 声道（4 麦 + 1 参考），普通声卡一般是 1 或 2。
func openCapture(device string, log func(string)) (*captureSource, error) {
	if log == nil {
		log = func(string) {}
	}
	var lastErr error
	for _, ch := range []int{5, 1, 2} {
		pcm, err := openALSA(device, true, 16000, ch)
		if err != nil {
			lastErr = err
			continue
		}
		log(fmt.Sprintf("麦克风已打开：%s %d 声道 %d Hz", device, pcm.ch, pcm.rate))
		return &captureSource{
			device:  device,
			backend: "alsa",
			ch:      pcm.ch,
			rate:    pcm.rate,
			pcm:     pcm,
			log:     log,
			stop:    make(chan struct{}),
			done:    make(chan struct{}),
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的采集参数")
	}
	return nil, lastErr
}

// start 起读取线程：硬件数据 → 混单声道 → 按 FrameSamples 切帧回调。
func (c *captureSource) start(onFrame func([]int16)) {
	go func() {
		defer close(c.done)
		// 一次读 20ms 的量，多读一点减少系统调用
		chunk := 320 * c.ch
		buf := make([]int16, chunk)
		frame := make([]int16, 0, core.FrameSamples)
		for {
			select {
			case <-c.stop:
				return
			default:
			}

			n, err := c.pcm.read(buf)
			if err != nil {
				c.log(fmt.Sprintf("采集出错：%v", err))
				return
			}
			if n <= 0 {
				continue
			}
			// 多声道混成单声道：直接取平均，够用且不会因为某一路坏掉整体没声
			frames := n / c.ch
			for i := 0; i < frames; i++ {
				var sum int32
				for k := 0; k < c.ch; k++ {
					sum += int32(buf[i*c.ch+k])
				}
				frame = append(frame, int16(sum/int32(c.ch)))
				if len(frame) == core.FrameSamples {
					out := make([]int16, core.FrameSamples)
					copy(out, frame)
					frame = frame[:0]
					onFrame(out)
				}
			}
		}
	}()
}

func (c *captureSource) close() {
	c.stopOnce.Do(func() { close(c.stop) })
	<-c.done
	c.pcm.close()
}

//go:build linux && cgo && alsa && !app

package player

/*
#cgo linux LDFLAGS: -lasound
#include <alsa/asoundlib.h>
#include <errno.h>
#include <stdlib.h>
#include <stdio.h>
#include <stdint.h>
#include <strings.h>

typedef struct {
	snd_pcm_t *pcm;
	unsigned int frame_bytes;
	// 驱动实际给的缓冲帧数。请求的 40000us 常被向上取整到周期边界
	// (实测能到 78-85ms)，排队基准必须用它，否则目标会低于真实地板。
	unsigned long buffer_frames;
} wa_alsa_pcm;

static int wa_alsa_configure(snd_pcm_t *pcm, unsigned int rate, unsigned int channels, unsigned long *buffer_frames) {
	snd_pcm_hw_params_t *hw;
	snd_pcm_hw_params_alloca(&hw);
	int err = snd_pcm_hw_params_any(pcm, hw);
	if (err < 0) return err;
	if ((err = snd_pcm_hw_params_set_access(pcm, hw, SND_PCM_ACCESS_RW_INTERLEAVED)) < 0) return err;
	if ((err = snd_pcm_hw_params_set_format(pcm, hw, SND_PCM_FORMAT_S16_LE)) < 0) return err;
	if ((err = snd_pcm_hw_params_set_channels(pcm, hw, channels)) < 0) return err;

	unsigned int actual_rate = rate;
	int dir = 0;
	if ((err = snd_pcm_hw_params_set_rate_near(pcm, hw, &actual_rate, &dir)) < 0) return err;

	unsigned int period_time = 10000;
	dir = 0;
	if ((err = snd_pcm_hw_params_set_period_time_near(pcm, hw, &period_time, &dir)) < 0) return err;
	unsigned int buffer_time = 40000;
	dir = 0;
	if ((err = snd_pcm_hw_params_set_buffer_time_near(pcm, hw, &buffer_time, &dir)) < 0) return err;
	if ((err = snd_pcm_hw_params(pcm, hw)) < 0) return err;

	snd_pcm_uframes_t period_size = 0;
	snd_pcm_uframes_t buffer_size = 0;
	if ((err = snd_pcm_hw_params_get_period_size(hw, &period_size, &dir)) < 0) return err;
	if ((err = snd_pcm_hw_params_get_buffer_size(hw, &buffer_size)) < 0) return err;
	*buffer_frames = (unsigned long)buffer_size;

	snd_pcm_sw_params_t *sw;
	snd_pcm_sw_params_alloca(&sw);
	if ((err = snd_pcm_sw_params_current(pcm, sw)) < 0) return err;
	if ((err = snd_pcm_sw_params_set_avail_min(pcm, sw, period_size)) < 0) return err;
	if ((err = snd_pcm_sw_params_set_start_threshold(pcm, sw, 1)) < 0) return err;
	if ((err = snd_pcm_sw_params_set_stop_threshold(pcm, sw, buffer_size)) < 0) return err;
	return snd_pcm_sw_params(pcm, sw);
}

static wa_alsa_pcm *wa_alsa_open(unsigned int rate, unsigned int channels, const char *device, int *err) {
	wa_alsa_pcm *ctx = (wa_alsa_pcm *)calloc(1, sizeof(*ctx));
	if (ctx == NULL) {
		*err = -ENOMEM;
		return NULL;
	}
	*err = snd_pcm_open(&ctx->pcm, device, SND_PCM_STREAM_PLAYBACK, 0);
	if (*err < 0) {
		free(ctx);
		return NULL;
	}
	unsigned long buffer_frames = 0;
	*err = wa_alsa_configure(ctx->pcm, rate, channels, &buffer_frames);
	if (*err < 0) {
		snd_pcm_close(ctx->pcm);
		free(ctx);
		return NULL;
	}
	ctx->frame_bytes = channels * sizeof(int16_t);
	ctx->buffer_frames = buffer_frames;
	return ctx;
}

// 驱动实际给的缓冲帧数，0 表示未知。
static unsigned long wa_alsa_buffer_frames(wa_alsa_pcm *ctx) {
	if (ctx == NULL) {
		return 0;
	}
	return ctx->buffer_frames;
}

static long wa_alsa_write(wa_alsa_pcm *ctx, void *data, unsigned long frames) {
	unsigned long written = 0;
	char *ptr = (char *)data;
	while (written < frames) {
		snd_pcm_sframes_t n = snd_pcm_writei(ctx->pcm, ptr + written * ctx->frame_bytes, frames - written);
		if (n < 0) {
			n = snd_pcm_recover(ctx->pcm, (int)n, 1);
			if (n < 0) {
				return n;
			}
			continue;
		}
		if (n == 0) {
			continue;
		}
		written += (unsigned long)n;
	}
	return (long)written;
}

static const char *wa_alsa_error(long err) {
	return snd_strerror((int)err);
}

// 已写进驱动、还没播出来的帧数(声卡侧排队)。ALSA 自己知道这个数，
// 比 aplay 那边靠管道积压猜要准。查不到(比如刚 xrun)返回 -1。
static long wa_alsa_delay(wa_alsa_pcm *ctx) {
	if (ctx == NULL || ctx->pcm == NULL) {
		return -1;
	}
	snd_pcm_sframes_t delay = 0;
	if (snd_pcm_delay(ctx->pcm, &delay) < 0 || delay < 0) {
		return -1;
	}
	return (long)delay;
}

static void wa_alsa_drop(wa_alsa_pcm *ctx) {
	if (ctx != NULL && ctx->pcm != NULL) {
		snd_pcm_drop(ctx->pcm);
	}
}

static void wa_alsa_close(wa_alsa_pcm *ctx, int drain) {
	if (ctx == NULL) {
		return;
	}
	if (ctx->pcm != NULL) {
		if (drain) {
			snd_pcm_drain(ctx->pcm);
		} else {
			snd_pcm_drop(ctx->pcm);
		}
		snd_pcm_close(ctx->pcm);
	}
	free(ctx);
}

static snd_mixer_elem_t *wa_alsa_find_mixer_elem(snd_mixer_t *mixer) {
	static const char *preferred[] = {
		"Master Playback Volume",
		"PCM Playback Volume",
		"Speaker Playback Volume",
		"Master",
		"PCM",
		"Speaker",
	};
	snd_mixer_elem_t *elem;
	int i;
	for (i = 0; i < (int)(sizeof(preferred) / sizeof(preferred[0])); i++) {
		for (elem = snd_mixer_first_elem(mixer); elem != NULL; elem = snd_mixer_elem_next(elem)) {
			if (!snd_mixer_selem_is_active(elem) || !snd_mixer_selem_has_playback_volume(elem)) {
				continue;
			}
			if (strcasecmp(snd_mixer_selem_get_name(elem), preferred[i]) == 0) {
				return elem;
			}
		}
	}
	for (elem = snd_mixer_first_elem(mixer); elem != NULL; elem = snd_mixer_elem_next(elem)) {
		if (snd_mixer_selem_is_active(elem) && snd_mixer_selem_has_playback_volume(elem)) {
			return elem;
		}
	}
	return NULL;
}

static int wa_alsa_open_mixer(snd_mixer_t **out) {
	snd_mixer_t *mixer = NULL;
	int err = snd_mixer_open(&mixer, 0);
	if (err < 0) {
		return err;
	}
	int card = -1;
	err = snd_card_next(&card);
	if (err < 0 || card < 0) {
		snd_mixer_close(mixer);
		return err < 0 ? err : -ENODEV;
	}
	char card_name[32];
	snprintf(card_name, sizeof(card_name), "hw:%d", card);
	err = snd_mixer_attach(mixer, card_name);
	if (err < 0) {
		err = snd_mixer_attach(mixer, "default");
	}
	if (err >= 0) {
		err = snd_mixer_selem_register(mixer, NULL, NULL);
	}
	if (err >= 0) {
		err = snd_mixer_load(mixer);
	}
	if (err < 0) {
		snd_mixer_close(mixer);
		return err;
	}
	*out = mixer;
	return 0;
}

static int wa_alsa_mixer_set(long percent) {
	snd_mixer_t *mixer = NULL;
	int err = wa_alsa_open_mixer(&mixer);
	if (err < 0) {
		return err;
	}
	snd_mixer_elem_t *elem = wa_alsa_find_mixer_elem(mixer);
	if (elem == NULL) {
		snd_mixer_close(mixer);
		return -ENODEV;
	}
	long min = 0;
	long max = 0;
	err = snd_mixer_selem_get_playback_volume_range(elem, &min, &max);
	if (err >= 0) {
		long value = min + (max - min) * percent / 100;
		err = snd_mixer_selem_set_playback_volume_all(elem, value);
	}
	snd_mixer_close(mixer);
	return err;
}

static int wa_alsa_mixer_change(long delta) {
	snd_mixer_t *mixer = NULL;
	int err = wa_alsa_open_mixer(&mixer);
	if (err < 0) {
		return err;
	}
	snd_mixer_elem_t *elem = wa_alsa_find_mixer_elem(mixer);
	if (elem == NULL) {
		snd_mixer_close(mixer);
		return -ENODEV;
	}
	long min = 0;
	long max = 0;
	long current = 0;
	err = snd_mixer_selem_get_playback_volume_range(elem, &min, &max);
	if (err >= 0) {
		err = snd_mixer_selem_get_playback_volume(elem, SND_MIXER_SCHN_FRONT_LEFT, &current);
	}
	if (err >= 0) {
		long value = current + (max - min) * delta / 100;
		if (value < min) value = min;
		if (value > max) value = max;
		err = snd_mixer_selem_set_playback_volume_all(elem, value);
	}
	snd_mixer_close(mixer);
	return err;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/hajimehoshi/go-mp3"
)

var (
	alsaPCMmu      sync.Mutex
	alsaCurrentPCM *C.wa_alsa_pcm
	alsaConfigOnce sync.Once
)

func ensureALSAConfig() {
	alsaConfigOnce.Do(func() {
		if os.Getenv("ALSA_CONFIG_PATH") != "" {
			return
		}
		if _, err := os.Stat("/usr/share/alsa/alsa.conf"); err == nil {
			return
		}
		if _, err := os.Stat("/etc/asound.conf"); err == nil {
			_ = os.Setenv("ALSA_CONFIG_PATH", "/etc/asound.conf")
		}
	})
}

func alsaBackendAvailable() bool {
	ensureALSAConfig()
	if matches, _ := filepath.Glob("/dev/snd/pcmC*D*p"); len(matches) == 0 {
		return false
	}
	for _, path := range []string{
		"/lib/libasound.so.2",
		"/lib64/libasound.so.2",
		"/usr/lib/libasound.so.2",
		"/usr/lib64/libasound.so.2",
		"/usr/lib32/libasound.so.2",
	} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func alsaPlayURL(url string) error {
	ensureALSAConfig()
	stream, err := openMP3(url)
	if err != nil {
		return err
	}
	if playbackCanceled(url) || !setPCMStream(url, stream) {
		_ = stream.Close()
		return fmt.Errorf("playback canceled")
	}
	defer func() {
		_ = stream.Close()
		clearPCMStream(stream)
	}()

	decoder, err := mp3.NewDecoder(stream)
	if err != nil {
		return fmt.Errorf("decode mp3: %w", err)
	}

	var openErr C.int
	device := C.CString(alsaDevice())
	defer C.free(unsafe.Pointer(device))
	pcm := C.wa_alsa_open(C.uint(decoder.SampleRate()), 2, device, &openErr)
	if pcm == nil {
		return fmt.Errorf("alsa open %s: %s", alsaDevice(), C.GoString(C.wa_alsa_error(C.long(openErr))))
	}
	alsaPCMmu.Lock()
	alsaCurrentPCM = pcm
	alsaPCMmu.Unlock()
	completed := false
	defer func() {
		alsaPCMmu.Lock()
		if alsaCurrentPCM == pcm {
			alsaCurrentPCM = nil
		}
		alsaPCMmu.Unlock()
		C.wa_alsa_close(pcm, C.int(boolInt(completed)))
	}()

	readBuffer := make([]byte, 64*1024)
	pending := make([]byte, 0, 4)
	for {
		for IsPaused && !playbackCanceled(url) {
			time.Sleep(50 * time.Millisecond)
		}
		if playbackCanceled(url) {
			return fmt.Errorf("playback canceled")
		}

		n, readErr := decoder.Read(readBuffer)
		if n > 0 {
			pending = append(pending, readBuffer[:n]...)
			aligned := len(pending) &^ 3
			if aligned > 0 {
				frames := C.ulong(aligned / 4)
				written := C.wa_alsa_write(pcm, unsafe.Pointer(&pending[0]), frames)
				if written < 0 {
					return fmt.Errorf("alsa write: %s", C.GoString(C.wa_alsa_error(C.long(written))))
				}
				pending = append(pending[:0], pending[aligned:]...)
			}
		}
		if readErr == io.EOF {
			completed = true
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read decoded pcm: %w", readErr)
		}
	}
}

func alsaPlayPCMStream(r io.Reader, rate, channels int, remoteAddr string) error {
	ensureALSAConfig()
	var openErr C.int
	device := C.CString(alsaDevice())
	defer C.free(unsafe.Pointer(device))
	pcm := C.wa_alsa_open(C.uint(rate), C.uint(channels), device, &openErr)
	if pcm == nil {
		return fmt.Errorf("alsa open %s: %s", alsaDevice(), C.GoString(C.wa_alsa_error(C.long(openErr))))
	}
	alsaPCMmu.Lock()
	alsaCurrentPCM = pcm
	alsaPCMmu.Unlock()

	completed := false
	defer func() {
		alsaPCMmu.Lock()
		if alsaCurrentPCM == pcm {
			alsaCurrentPCM = nil
		}
		alsaPCMmu.Unlock()
		C.wa_alsa_close(pcm, C.int(boolInt(completed)))
	}()

	frameBytes := channels * 2
	// 用驱动实际给的缓冲当排队基准：请求 40000us 常被向上取整到周期边界
	// (实测 78-85ms)，写死 40ms 会让目标低于真实地板，跳读永远在触发。
	bufferBytes := int64(C.wa_alsa_buffer_frames(pcm)) * int64(frameBytes)
	if bufferBytes <= 0 {
		bufferBytes = msBytes(rate, frameBytes, pcmSinkBufferMs)
	}
	queue := newPCMQueue(rate, frameBytes, bufferBytes)
	// 原来是 64KB(≈370ms)：wa_alsa_write 是全量阻塞写，一次塞 370ms 进声卡缓冲，
	// 要塞 300 多毫秒，这期间进程根本腾不出手去丢包。
	readBuffer := make([]byte, msBytes(rate, frameBytes, pcmReadChunkMs))
	pending := make([]byte, 0, len(readBuffer)+frameBytes)
	emptyReads := 0
	log.Printf("alsa: 实时流 %dHz %d声道，声卡缓冲 %dms，目标排队 %dms",
		rate, channels, queue.ms(bufferBytes), queue.ms(queue.target))
	for {
		// 先量后排、再读：只有还没进声卡的旧音频才丢得掉
		socketBytes := unreadBytes(remoteAddr)
		if err := queue.fix(r, socketBytes, alsaQueuedBytes(pcm, frameBytes, rate)); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				completed = true
				return nil
			}
			return fmt.Errorf("skip PCM stream: %w", err)
		}

		n, readErr := queue.read(r, readBuffer, alsaQueuedBytes(pcm, frameBytes, rate))
		if n > 0 {
			emptyReads = 0
			pending = append(pending, readBuffer[:n]...)
			aligned := len(pending) / frameBytes * frameBytes
			if aligned > 0 {
				frames := C.ulong(aligned / frameBytes)
				written := C.wa_alsa_write(pcm, unsafe.Pointer(&pending[0]), frames)
				if written < 0 {
					return fmt.Errorf("alsa write: %s", C.GoString(C.wa_alsa_error(C.long(written))))
				}
				queue.writtenBytes += int64(aligned)
				pending = append(pending[:0], pending[aligned:]...)
			}
		}
		if n == 0 && readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return io.ErrNoProgress
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				if len(pending) != 0 {
					return fmt.Errorf("incomplete PCM frame: received %d trailing bytes, expected a multiple of %d", len(pending), frameBytes)
				}
				completed = true
				return nil
			}
			return fmt.Errorf("read PCM stream: %w", readErr)
		}
	}
}

// alsaQueuedBytes 返回声卡侧当前排队的字节数，查不到就按缓冲满来估。
func alsaQueuedBytes(pcm *C.wa_alsa_pcm, frameBytes, rate int) int64 {
	delay := C.wa_alsa_delay(pcm)
	if delay < 0 {
		return msBytes(rate, frameBytes, pcmSinkBufferMs)
	}
	return int64(delay) * int64(frameBytes)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func alsaCancelPlayback() {
	alsaPCMmu.Lock()
	defer alsaPCMmu.Unlock()
	if alsaCurrentPCM != nil {
		C.wa_alsa_drop(alsaCurrentPCM)
	}
}

func alsaPausePlayback() error {
	return nil
}

func alsaResumePlayback() error {
	return nil
}

func alsaSetVolume(value string) error {
	ensureALSAConfig()
	volume, err := parseALSAVolume(value)
	if err != nil {
		return err
	}
	if mixerErr := C.wa_alsa_mixer_set(C.long(volume * 100)); mixerErr < 0 {
		return fmt.Errorf("alsa mixer set: %s", C.GoString(C.wa_alsa_error(C.long(mixerErr))))
	}
	return nil
}

func alsaChangeVolume(value string) error {
	ensureALSAConfig()
	trimmed := strings.TrimSpace(value)
	delta := 1.0
	if strings.HasSuffix(trimmed, "%-") {
		delta = -1
		trimmed = strings.TrimSuffix(trimmed, "%-")
	} else if strings.HasSuffix(trimmed, "%+") {
		trimmed = strings.TrimSuffix(trimmed, "%+")
	} else {
		return alsaSetVolume(trimmed)
	}
	amount, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return fmt.Errorf("invalid ALSA volume %q: %w", value, err)
	}
	if mixerErr := C.wa_alsa_mixer_change(C.long(delta * amount)); mixerErr < 0 {
		return fmt.Errorf("alsa mixer change: %s", C.GoString(C.wa_alsa_error(C.long(mixerErr))))
	}
	return nil
}

func parseALSAVolume(value string) (float64, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(value, "%"))
	percent, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || percent < 0 || percent > 100 {
		return 0, fmt.Errorf("invalid ALSA volume %q", value)
	}
	return percent / 100, nil
}

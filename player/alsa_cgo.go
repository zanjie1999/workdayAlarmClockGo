//go:build linux && cgo

package player

/*
#cgo linux LDFLAGS: -lasound
#include <alsa/asoundlib.h>
#include <errno.h>
#include <stdlib.h>
#include <stdio.h>
#include <strings.h>

typedef struct {
	snd_pcm_t *pcm;
} wa_alsa_pcm;

static wa_alsa_pcm *wa_alsa_open(unsigned int rate, int *err) {
	wa_alsa_pcm *ctx = (wa_alsa_pcm *)calloc(1, sizeof(*ctx));
	if (ctx == NULL) {
		*err = -ENOMEM;
		return NULL;
	}
	*err = snd_pcm_open(&ctx->pcm, "default", SND_PCM_STREAM_PLAYBACK, 0);
	if (*err < 0) {
		free(ctx);
		return NULL;
	}
	*err = snd_pcm_set_params(
		ctx->pcm,
		SND_PCM_FORMAT_S16_LE,
		SND_PCM_ACCESS_RW_INTERLEAVED,
		2,
		rate,
		1,
		500000
	);
	if (*err < 0) {
		snd_pcm_close(ctx->pcm);
		free(ctx);
		return NULL;
	}
	return ctx;
}

static long wa_alsa_write(wa_alsa_pcm *ctx, void *data, unsigned long frames) {
	unsigned long written = 0;
	char *ptr = (char *)data;
	while (written < frames) {
		snd_pcm_sframes_t n = snd_pcm_writei(ctx->pcm, ptr + written * 4, frames - written);
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
	"fmt"
	"io"
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
	pcm := C.wa_alsa_open(C.uint(decoder.SampleRate()), &openErr)
	if pcm == nil {
		return fmt.Errorf("alsa open default: %s", C.GoString(C.wa_alsa_error(C.long(openErr))))
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

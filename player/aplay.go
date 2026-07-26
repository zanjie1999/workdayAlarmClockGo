//go:build linux

package player

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/hajimehoshi/go-mp3"
	"github.com/zanjie1999/httpme"
)

var (
	pcmStreamMu sync.Mutex
	pcmStream   io.ReadCloser
)

func defaultShellPlayer() string {
	return "aplay"
}

func usePCMPlayer() bool {
	name := filepath.Base(ShellPlayer)
	return name == "aplay" || name == "kindle"
}

func isKindle() bool {
	return filepath.Base(ShellPlayer) == "kindle"
}

func pcmURL(url string) error {
	stream, err := openMP3(url)
	if err != nil {
		return err
	}
	if (IsStop && !IsPlayWeather) || NowUrl != url {
		stream.Close()
		return fmt.Errorf("playback canceled")
	}
	if !setPCMStream(url, stream) {
		stream.Close()
		return fmt.Errorf("playback canceled")
	}
	defer func() {
		stream.Close()
		clearPCMStream(stream)
	}()
	if (IsStop && !IsPlayWeather) || NowUrl != url {
		return fmt.Errorf("playback canceled")
	}

	decoder, err := mp3.NewDecoder(stream)
	if err != nil {
		return fmt.Errorf("decode mp3: %w", err)
	}
	cmd := pcmCommand(decoder.SampleRate())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = startUnixCmd(cmd); err != nil {
		return err
	}
	if IsPaused {
		_ = signalUnixCmd(syscall.SIGSTOP)
	}

	_, copyErr := io.Copy(stdin, decoder)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	clearUnixCmd(cmd)
	if (IsStop && !IsPlayWeather) || NowUrl != url {
		return fmt.Errorf("playback canceled")
	}
	if waitErr != nil {
		return waitErr
	}
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func pcmCommand(sampleRate int) *exec.Cmd {
	rate := strconv.Itoa(sampleRate)
	if isKindle() {
		return exec.Command("/usr/bin/gst-launch",
			"filesrc", "location=/dev/stdin",
			"!", "audio/x-raw-int,",
			"endianness=(int)1234,",
			"signed=(boolean)true,",
			"width=(int)16,",
			"depth=(int)16,",
			"rate=(int)"+rate+",",
			"channels=(int)2",
			"!", "queue", "!", "mixersink",
		)
	}
	return exec.Command("aplay", "-q", "-t", "raw", "-f", "S16_LE", "-c", "2", "-r", rate)
}

func openMP3(url string) (io.ReadCloser, error) {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		resp, err := httpme.GetStream(url)
		if err != nil {
			return nil, err
		}
		if resp.R.StatusCode < http.StatusOK || resp.R.StatusCode >= http.StatusMultipleChoices {
			resp.R.Body.Close()
			return nil, fmt.Errorf("download status: %s", resp.R.Status)
		}
		return resp.R.Body, nil
	}
	return os.Open(url)
}

func setPCMStream(url string, stream io.ReadCloser) bool {
	pcmStreamMu.Lock()
	defer pcmStreamMu.Unlock()
	if (IsStop && !IsPlayWeather) || NowUrl != url {
		return false
	}
	if pcmStream != nil {
		_ = pcmStream.Close()
	}
	pcmStream = stream
	return true
}

func clearPCMStream(stream io.ReadCloser) {
	pcmStreamMu.Lock()
	defer pcmStreamMu.Unlock()
	if pcmStream == stream {
		pcmStream = nil
	}
}

func cancelPlatformPlayback() {
	pcmStreamMu.Lock()
	defer pcmStreamMu.Unlock()
	if pcmStream != nil {
		_ = pcmStream.Close()
		pcmStream = nil
	}
}

func pausePlatformPlayback() error {
	return signalUnixCmd(syscall.SIGSTOP)
}

func resumePlatformPlayback() error {
	return signalUnixCmd(syscall.SIGCONT)
}

func setPlatformVolume(value string) error {
	if isKindle() {
		return nil
	}
	return runAmixer(strings.TrimSuffix(value, "%") + "%")
}

func changePlatformVolume(value string) error {
	if isKindle() {
		return nil
	}
	return runAmixer(value)
}

func runAmixer(value string) error {
	var lastErr error
	for _, control := range []string{"Master", "PCM", "Speaker"} {
		if err := exec.Command("amixer", "-q", "sset", control, value).Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("amixer Master/PCM/Speaker: %w", lastErr)
}

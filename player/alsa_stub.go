//go:build !linux || !cgo || !alsa || app

package player

import (
	"errors"
	"io"
)

func alsaBackendAvailable() bool {
	return false
}

func alsaPlayURL(string) error {
	return errors.New("direct ALSA backend is unavailable in this build")
}

func alsaPlayPCMStream(io.Reader, int, int) error {
	return ErrPCMStreamUnsupported
}

func alsaCancelPlayback() {}

func alsaPausePlayback() error {
	return errors.New("direct ALSA backend is unavailable in this build")
}

func alsaResumePlayback() error {
	return errors.New("direct ALSA backend is unavailable in this build")
}

func alsaSetVolume(string) error {
	return errors.New("direct ALSA backend is unavailable in this build")
}

func alsaChangeVolume(string) error {
	return errors.New("direct ALSA backend is unavailable in this build")
}

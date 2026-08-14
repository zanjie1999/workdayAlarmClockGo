//go:build !linux || !cgo

package player

import "errors"

func alsaBackendAvailable() bool {
	return false
}

func alsaPlayURL(string) error {
	return errors.New("direct ALSA backend is unavailable in this build")
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

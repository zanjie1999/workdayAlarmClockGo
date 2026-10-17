package player

import (
	"errors"
	"fmt"
	"io"
)

var ErrPCMStreamUnsupported = errors.New("raw PCM streaming requires Linux and aplay or the direct ALSA backend")

// PlayPCMStream plays an incoming raw PCM stream without buffering it to disk.
// remoteAddr 用来查这条连接的内核接收缓冲里还压着多少没读的旧音频。
func PlayPCMStream(r io.Reader, rate, channels int, remoteAddr string) error {
	if rate < 8000 || rate > 192000 {
		return fmt.Errorf("rate must be between 8000 and 192000 Hz")
	}
	if channels < 1 || channels > 8 {
		return fmt.Errorf("channels must be between 1 and 8")
	}
	return playPCMStream(r, rate, channels, remoteAddr)
}

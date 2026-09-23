package player

import (
	"errors"
	"fmt"
	"io"
)

var ErrPCMStreamUnsupported = errors.New("raw PCM streaming requires Linux and aplay")

// PlayPCMStream pipes an incoming raw PCM stream to aplay without buffering it
// to disk. The aplay process ends when the input ends or its request disconnects.
func PlayPCMStream(r io.Reader, rate, channels int) error {
	if rate < 8000 || rate > 192000 {
		return fmt.Errorf("rate must be between 8000 and 192000 Hz")
	}
	if channels < 1 || channels > 8 {
		return fmt.Errorf("channels must be between 1 and 8")
	}
	return playPCMStream(r, rate, channels)
}

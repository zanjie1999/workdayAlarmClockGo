//go:build !linux

package player

import "io"

func playPCMStream(io.Reader, int, int) error {
	return ErrPCMStreamUnsupported
}

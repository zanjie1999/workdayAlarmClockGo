//go:build !linux || app

package player

import "io"

func playPCMStream(io.Reader, int, int) error {
	return ErrPCMStreamUnsupported
}

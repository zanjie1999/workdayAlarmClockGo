//go:build !linux || app

package player

import "io"

func playPCMStream(io.Reader, int, int, string) error {
	return ErrPCMStreamUnsupported
}

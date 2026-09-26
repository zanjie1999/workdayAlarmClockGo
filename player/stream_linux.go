//go:build linux

package player

import (
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
)

func playPCMStream(r io.Reader, rate, channels int) error {
	if filepath.Base(ShellPlayer) != "aplay" {
		return ErrPCMStreamUnsupported
	}
	cmd := exec.Command(ShellPlayer, "-q", "-t", "raw", "-f", "S16_LE",
		"-c", strconv.Itoa(channels), "-r", strconv.Itoa(rate),
		"-B", "80000", "-F", "20000")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	_, copyErr := io.Copy(stdin, r)
	closeErr := stdin.Close()
	if copyErr != nil {
		// A broken request should stop playback immediately rather than drain
		// audio already queued in aplay's ALSA buffer.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}

//go:build !linux

package player

func defaultShellPlayer() string {
	return "play"
}

func usePCMPlayer() bool {
	return false
}

func pcmURL(string) error {
	return nil
}

func cancelPlatformPlayback() {}

func pausePlatformPlayback() error {
	return nil
}

func resumePlatformPlayback() error {
	return nil
}

func setPlatformVolume(string) error {
	return nil
}

func changePlatformVolume(string) error {
	return nil
}

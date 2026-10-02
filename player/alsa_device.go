package player

import (
	"os/exec"
	"strings"
)

var alsaDeviceName string

func detectALSADevice() string {
	path, err := exec.LookPath("aplay")
	if err != nil {
		return "default"
	}
	out, err := exec.Command(path, "-L").Output()
	if err != nil {
		return "default"
	}
	return parseALSADeviceList(string(out))
}

func parseALSADeviceList(out string) string {
	for _, line := range strings.Split(out, "\n") {
		// aplay -L indents descriptions; only unindented lines name devices.
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "null" {
			continue
		}
		return fields[0]
	}
	return "default"
}

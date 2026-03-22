package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// ResultJsonWriteCmd builds a shell command that creates mountName/ and writes
// data to mountName/result.json without relying on stdin.
// The data bytes are embedded directly in the command using POSIX single-quote
// escaping so the command is safe at any shell-nesting depth (e.g. Fly's
// nested sh -c chain where stdin is not piped through to the inner process).
func ResultJsonWriteCmd(mountName string, data []byte) string {
	escaped := "'" + strings.ReplaceAll(string(data), "'", `'\''`) + "'"
	return fmt.Sprintf("mkdir -p %s && printf '%%s' %s > %s/result.json",
		strconv.Quote(mountName), escaped, strconv.Quote(mountName))
}

// ResolveOutputMountPath maps host-path-like values back to mount names used in sandbox.
func ResolveOutputMountPath(config AgentConfig) string {
	value := strings.TrimSpace(config.OutputVolumePath)
	if value == "" {
		return ""
	}

	if _, ok := config.Mounts[value]; ok {
		return value
	}

	for mountPath, volume := range config.Mounts {
		if volume.Path == value || volume.Name == value {
			return mountPath
		}
	}

	return value
}

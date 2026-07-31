package core

import (
	"path/filepath"
	"strings"
)

// ResolveRunDir returns the runtime state directory for sockets and other
// ephemeral files. When configuredRunDir is non-empty it is used as-is;
// otherwise the default is <dataDir>/run.
func ResolveRunDir(dataDir, configuredRunDir string) string {
	configuredRunDir = strings.TrimSpace(configuredRunDir)
	if configuredRunDir != "" {
		return configuredRunDir
	}
	return filepath.Join(dataDir, "run")
}

package chatapi

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolvePrivilegedPath resolves a privileged file path relative to workspace.
// Relative paths (including leading "./") join under workspace; "~" / "~/" expand
// via expandHomeDir; absolute paths are cleaned as-is. ".." may leave the workspace.
func resolvePrivilegedPath(workspace, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("chat-api: privileged path is empty")
	}
	expanded, err := expandHomeDir(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(workspace, expanded)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("chat-api: resolve privileged path: %w", err)
	}
	return filepath.Clean(abs), nil
}

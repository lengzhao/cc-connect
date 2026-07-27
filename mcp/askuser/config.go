package askuser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chenhg5/cc-connect/core"
)

// WriteMCPConfig writes a Claude Code mcp config JSON for one session.
// When socketPath is set, Claude Code connects via Unix socket (url host is ignored).
func WriteMCPConfig(path, mcpURL, socketPath, sessionKey string) error {
	if path == "" || mcpURL == "" || sessionKey == "" {
		return fmt.Errorf("askuser: path, url, and session key required")
	}
	entry := map[string]any{
		"type": "http",
		"url":  mcpURL,
		"headers": map[string]string{
			core.SessionKeyHeader: sessionKey,
		},
		// 1 hour — user may take long on confirm cards.
		"timeout": 3600000,
	}
	if socketPath != "" {
		entry["socketPath"] = socketPath
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			serverName: entry,
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

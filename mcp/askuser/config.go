package askuser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	EnvAskUserSocketPath = "CC_CONNECT_ASKUSER_SOCKET"
	EnvSessionKey        = "CC_CONNECT_SESSION_KEY"
)

// WriteMCPConfig writes a Claude Code stdio MCP config JSON for one session.
// Claude connects to the stdio bridge; the bridge talks to cc-connect over Unix socket.
func WriteMCPConfig(path, command, socketPath, sessionKey string) error {
	if path == "" || command == "" || socketPath == "" || sessionKey == "" {
		return fmt.Errorf("askuser: path, command, socket path, and session key required")
	}
	entry := map[string]any{
		"type":    "stdio",
		"command": command,
		"args":    []string{"askuser-mcp-stdio"},
		"env": map[string]string{
			EnvAskUserSocketPath: socketPath,
			EnvSessionKey:        sessionKey,
		},
		// 1 hour — user may take long on confirm cards.
		"timeout": 3600000,
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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

package config

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	sessionStoreJSONLChannel = "jsonl_channel"
	defaultChatAPIKeyPrefix  = "chat-api:"
)

// SessionStoreSettings holds resolved session persistence options for one project.
type SessionStoreSettings struct {
	UseJSONLChannel bool
	Dir             string
	KeyPrefix       string
}

// DefaultChatAPIRecordsDir is the default JSONL shard root for chat-api projects.
func DefaultChatAPIRecordsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".cc-connect", "chat-api", "records")
	}
	return filepath.Join(home, ".cc-connect", "chat-api", "records")
}

// HasPlatformType reports whether the project registers a platform of the given type.
func (p ProjectConfig) HasPlatformType(platformType string) bool {
	want := strings.ToLower(strings.TrimSpace(platformType))
	for _, plat := range p.Platforms {
		if strings.EqualFold(strings.TrimSpace(plat.Type), want) {
			return true
		}
	}
	return false
}

// SessionStoreSettings resolves session_store* for a project. When the project
// includes chat-api and session_store is omitted, jsonl_channel under
// DefaultChatAPIRecordsDir() is used automatically.
func (p ProjectConfig) SessionStoreSettings() SessionStoreSettings {
	mode := strings.ToLower(strings.TrimSpace(p.SessionStore))
	if mode == "" && p.HasPlatformType("chat-api") {
		mode = sessionStoreJSONLChannel
	}
	if mode != sessionStoreJSONLChannel {
		return SessionStoreSettings{}
	}

	dir := strings.TrimSpace(p.SessionStoreDir)
	if dir == "" {
		dir = DefaultChatAPIRecordsDir()
	}
	prefix := strings.TrimSpace(p.SessionStoreKeyPrefix)
	if prefix == "" {
		prefix = defaultChatAPIKeyPrefix
	}
	return SessionStoreSettings{
		UseJSONLChannel: true,
		Dir:             dir,
		KeyPrefix:       prefix,
	}
}

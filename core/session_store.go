package core

import "strings"

// SessionStoreMode selects how SessionManager persists state to disk.
type SessionStoreMode string

const (
	SessionStoreJSON         SessionStoreMode = "json"
	SessionStoreJSONLChannel SessionStoreMode = "jsonl_channel"
)

// SessionStoreConfig configures optional JSONL channel sharding for session keys
// matching KeyPrefix (e.g. "chat-api:"). Other keys continue using the JSON
// snapshot at StorePath when Mode is jsonl_channel.
type SessionStoreConfig struct {
	Mode      SessionStoreMode
	Dir       string
	KeyPrefix string
}

// ParseSessionStoreMode parses a config string; unknown values default to json.
func ParseSessionStoreMode(raw string) SessionStoreMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(SessionStoreJSONLChannel):
		return SessionStoreJSONLChannel
	default:
		return SessionStoreJSON
	}
}

func (c SessionStoreConfig) usesJSONLChannel() bool {
	return c.Mode == SessionStoreJSONLChannel && strings.TrimSpace(c.Dir) != "" && c.KeyPrefix != ""
}

func (c SessionStoreConfig) matchesKey(userKey string) bool {
	return c.KeyPrefix != "" && strings.HasPrefix(userKey, c.KeyPrefix)
}

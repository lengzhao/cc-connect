package chatapi

import "strings"

// channelKeyForMessage resolves the workspace channel for a chat message.
// An omitted channel header falls back to userID so each user gets an isolated
// workspace instead of sharing a global default channel.
func channelKeyForMessage(headerChannel, userID string) string {
	if ch := strings.TrimSpace(headerChannel); ch != "" {
		return ch
	}
	return userID
}

// ResolveChannelName implements core.ChannelNameResolver for multi-workspace mode.
// Valid channels return themselves for base_dir/<channel> convention matching.
func (p *Platform) ResolveChannelName(channelID string) (string, error) {
	if name, ok := workspaceChannelName(channelID); ok {
		return name, nil
	}
	return "", nil
}

func workspaceChannelName(channelID string) (string, bool) {
	if channelID == "" {
		return "", false
	}
	if !isSafeWorkspaceChannelPath(channelID) {
		return "", false
	}
	return channelID, true
}

// isSafeWorkspaceChannelPath rejects values that would escape or confuse
// base_dir/<channel> convention matching.
func isSafeWorkspaceChannelPath(channel string) bool {
	if channel == "" {
		return false
	}
	if strings.HasPrefix(channel, "/") || strings.HasSuffix(channel, "/") {
		return false
	}
	for _, seg := range strings.Split(channel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

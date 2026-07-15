package chatapi

import "strings"

// defaultWorkspaceChannelID is assigned at the API entry layer when the client
// omits X-Chat-API-Channel. It is a real channel (base_dir/default_channel), not
// a sentinel mapped to the project root.
const defaultWorkspaceChannelID = "default_channel"

// channelKeyForMessage maps an omitted channel header to the default workspace
// channel so Engine can convention-match base_dir/<channel>.
func (p *Platform) channelKeyForMessage(headerChannel string) string {
	if strings.TrimSpace(headerChannel) != "" {
		return headerChannel
	}
	return defaultWorkspaceChannelID
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

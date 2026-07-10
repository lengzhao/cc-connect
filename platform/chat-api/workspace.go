package chatapi

import "strings"

// defaultWorkspaceChannelID is an internal binding key used when the client omits
// X-Chat-API-Channel in multi-workspace mode. It is never exposed in the HTTP API.
const defaultWorkspaceChannelID = "__default__"

// channelKeyForMessage maps an omitted channel header to the internal default
// workspace channel. In multi-workspace mode ResolveChannelName maps that
// channel to "." so Engine's existing convention matcher resolves base_dir.
func (p *Platform) channelKeyForMessage(headerChannel string) string {
	if strings.TrimSpace(headerChannel) != "" {
		return headerChannel
	}
	return defaultWorkspaceChannelID
}

// ResolveChannelName implements core.ChannelNameResolver for multi-workspace mode.
// The default internal channel maps to "." (project base_dir). Explicit channels
// return their header value so Engine can convention-match base_dir/<channel>.
func (p *Platform) ResolveChannelName(channelID string) (string, error) {
	if channelID == defaultWorkspaceChannelID {
		return ".", nil
	}
	if name, ok := workspaceChannelName(channelID); ok {
		return name, nil
	}
	return "", nil
}

func workspaceChannelName(channelID string) (string, bool) {
	if channelID == "" || channelID == defaultWorkspaceChannelID {
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

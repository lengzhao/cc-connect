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

func (p *Platform) ResolveChannelName(channelID string) (string, error) {
	if channelID == defaultWorkspaceChannelID {
		return ".", nil
	}
	return "", nil
}

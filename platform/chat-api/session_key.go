package chatapi

import (
	"fmt"
	"strings"
)

const engineSessionKeyPrefix = "chat-api:"

// engineSessionKey builds the Engine/agent session key in the same spirit as
// Slack/Feishu: {platform}:{channel}:{isolator}. For chat-api the isolator is
// conversation_id (not user_id) so guests sharing a conversation_id reuse the
// same agent context.
func engineSessionKey(channelKey, conversationID string) string {
	if channelKey == "" {
		channelKey = defaultWorkspaceChannelID
	}
	return fmt.Sprintf("%s%s:%s", engineSessionKeyPrefix, encodeSessionChannelSegment(channelKey), conversationID)
}

func encodeSessionChannelSegment(channelKey string) string {
	// Colons in channel keys would break core.extractChannelID; escape them.
	return strings.ReplaceAll(channelKey, ":", "%3A")
}

// conversationIDFromEngineSessionKey recovers the opaque conversation id from
// an Engine session key. Returns empty when the key is not chat-api shaped.
func conversationIDFromEngineSessionKey(sessionKey string) string {
	if !strings.HasPrefix(sessionKey, engineSessionKeyPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(sessionKey, engineSessionKeyPrefix)
	idx := strings.LastIndex(rest, ":")
	if idx <= 0 {
		return ""
	}
	convID := rest[idx+1:]
	if !isOpaqueConversationID(convID) {
		return ""
	}
	return convID
}

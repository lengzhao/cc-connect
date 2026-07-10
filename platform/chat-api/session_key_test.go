package chatapi

import "testing"

func TestEngineSessionKey(t *testing.T) {
	tests := []struct {
		channel string
		conv    string
		want    string
	}{
		{defaultWorkspaceChannelID, "conv_abc123", "chat-api:__default__:conv_abc123"},
		{"chat-123", "conv_abc123", "chat-api:chat-123:conv_abc123"},
		{"team/backend", "conv_abc123", "chat-api:team/backend:conv_abc123"},
		{"team:alpha", "conv_abc123", "chat-api:team%3Aalpha:conv_abc123"},
	}
	for _, tt := range tests {
		if got := engineSessionKey(tt.channel, tt.conv); got != tt.want {
			t.Fatalf("engineSessionKey(%q, %q) = %q, want %q", tt.channel, tt.conv, got, tt.want)
		}
	}
}

func TestConversationIDFromEngineSessionKey(t *testing.T) {
	conv := "conv_abcdefghijklmnopqrstuv"
	key := engineSessionKey("chat-123", conv)
	if got := conversationIDFromEngineSessionKey(key); got != conv {
		t.Fatalf("got %q, want %q", got, conv)
	}
	if got := conversationIDFromEngineSessionKey(conv); got != "" {
		t.Fatalf("bare conv id should not parse, got %q", got)
	}
}

func TestEngineSessionKeyGuestSharesConversation(t *testing.T) {
	conv := "conv_abcdefghijklmnopqrstuv"
	ownerKey := engineSessionKey(defaultWorkspaceChannelID, conv)
	guestKey := engineSessionKey(defaultWorkspaceChannelID, conv)
	if ownerKey != guestKey {
		t.Fatalf("guest key %q != owner key %q", guestKey, ownerKey)
	}
}

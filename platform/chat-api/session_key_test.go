package chatapi

import "testing"

func TestEngineSessionKey(t *testing.T) {
	tests := []struct {
		channel string
		conv    string
		want    string
	}{
		{"default_channel", "conv_abc123", "chat-api:default_channel:conv_abc123"},
		{"", "conv_abc123", "chat-api::conv_abc123"},
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
	for _, channel := range []string{"chat-123", ""} {
		key := engineSessionKey(channel, conv)
		if got := conversationIDFromEngineSessionKey(key); got != conv {
			t.Fatalf("channel %q: got %q, want %q", channel, got, conv)
		}
	}
	if got := conversationIDFromEngineSessionKey(conv); got != "" {
		t.Fatalf("bare conv id should not parse, got %q", got)
	}
}

func TestEngineSessionKeyGuestSharesConversation(t *testing.T) {
	conv := "conv_abcdefghijklmnopqrstuv"
	ownerKey := engineSessionKey("", conv)
	guestKey := engineSessionKey("", conv)
	if ownerKey != guestKey {
		t.Fatalf("guest key %q != owner key %q", guestKey, ownerKey)
	}
}

package slack

import (
	"testing"
)

func TestNewReactionEmojiDefaults(t *testing.T) {
	p, err := New(map[string]any{
		"bot_token": "xoxb-test",
		"app_token": "xapp-test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	plat := p.(*Platform)
	if plat.reactionEmoji != "eyes" {
		t.Fatalf("reactionEmoji = %q, want eyes", plat.reactionEmoji)
	}
	if plat.doneEmoji != "white_check_mark" {
		t.Fatalf("doneEmoji = %q, want white_check_mark", plat.doneEmoji)
	}
}

func TestNewReactionEmojiNone(t *testing.T) {
	p, err := New(map[string]any{
		"bot_token":      "xoxb-test",
		"app_token":      "xapp-test",
		"reaction_emoji": "none",
		"done_emoji":     "none",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	plat := p.(*Platform)
	if plat.reactionEmoji != "" || plat.doneEmoji != "" {
		t.Fatalf("reactionEmoji=%q doneEmoji=%q, want empty", plat.reactionEmoji, plat.doneEmoji)
	}
}

func TestMessageReactionRefRequiresMessageTS(t *testing.T) {
	p := &Platform{}
	if _, ok := p.messageReactionRef(replyContext{channel: "C1", timestamp: "123.456"}); ok {
		t.Fatal("expected false when messageTS is empty")
	}
	ref, ok := p.messageReactionRef(replyContext{channel: "C1", messageTS: "123.456"})
	if !ok || ref.Channel != "C1" || ref.Timestamp != "123.456" {
		t.Fatalf("ref = %#v ok=%v", ref, ok)
	}
}

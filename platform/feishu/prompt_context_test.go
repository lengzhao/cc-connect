package feishu

import "testing"

func TestPromptChannelAttrs_IncludesChannelAndThread(t *testing.T) {
	p := &Platform{platformName: "lark"}
	attrs := p.PromptChannelAttrs(replyContext{
		chatID:   "oc_test",
		threadID: "omt_topic",
	})
	if len(attrs) != 2 {
		t.Fatalf("attrs = %#v, want channel_id and thread_id", attrs)
	}
	if attrs[0] != "channel_id=oc_test" {
		t.Fatalf("attrs[0] = %q", attrs[0])
	}
	if attrs[1] != "thread_id=omt_topic" {
		t.Fatalf("attrs[1] = %q", attrs[1])
	}
}

func TestPromptChannelAttrs_OmitsThreadWhenEmpty(t *testing.T) {
	p := &Platform{platformName: "lark"}
	attrs := p.PromptChannelAttrs(replyContext{chatID: "oc_test"})
	if len(attrs) != 1 || attrs[0] != "channel_id=oc_test" {
		t.Fatalf("attrs = %#v", attrs)
	}
}

func TestHookContext_ExposesLarkIds(t *testing.T) {
	p := &Platform{platformName: "lark"}
	ctx := p.HookContext(replyContext{
		messageID: "om_msg",
		chatID:    "oc_test",
		threadID:  "omt_topic",
	})
	if ctx.Context["channel_id"] != "oc_test" {
		t.Fatalf("channel_id = %#v", ctx.Context["channel_id"])
	}
	if ctx.Context["thread_id"] != "omt_topic" {
		t.Fatalf("thread_id = %#v", ctx.Context["thread_id"])
	}
	if ctx.Context["message_id"] != "om_msg" {
		t.Fatalf("message_id = %#v", ctx.Context["message_id"])
	}
}

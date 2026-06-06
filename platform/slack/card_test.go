package slack

import (
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
)

func decodeRenderedBlocks(t *testing.T, card *core.Card, sessionKey string) []map[string]any {
	t.Helper()
	blocks := renderCardBlocks(card, sessionKey)
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal blocks: %v", err)
	}
	return got
}

func TestPlatformImplementsCardInterfaces(t *testing.T) {
	p, err := New(map[string]any{
		"bot_token": "xoxb-test",
		"app_token": "xapp-test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := p.(core.CardSender); !ok {
		t.Fatal("expected core.CardSender")
	}
	if _, ok := p.(core.CardNavigable); !ok {
		t.Fatal("expected core.CardNavigable")
	}
	if _, ok := p.(core.CardRefresher); !ok {
		t.Fatal("expected core.CardRefresher")
	}
}

func TestEncodeDecodeActionValue(t *testing.T) {
	raw := encodeActionValue("nav:/help session", "slack:C1:U1", map[string]string{
		"perm_label": "ok",
	})
	action, sessionKey, extra := decodeActionValue(raw)
	if action != "nav:/help session" {
		t.Fatalf("action = %q", action)
	}
	if sessionKey != "slack:C1:U1" {
		t.Fatalf("sessionKey = %q", sessionKey)
	}
	if extra["perm_label"] != "ok" {
		t.Fatalf("extra = %#v", extra)
	}
}

func TestRenderCardBlocks_HeaderAndButtons(t *testing.T) {
	card := core.NewCard().
		Title("Help", "blue").
		Markdown("Choose a section").
		Buttons(core.PrimaryBtn("Sessions", "nav:/help session")).
		Build()

	blocks := decodeRenderedBlocks(t, card, "slack:C1:U1")
	if len(blocks) < 3 {
		t.Fatalf("blocks = %#v, want at least 3", blocks)
	}
	if blocks[0]["type"] != string(slack.MBTHeader) {
		t.Fatalf("first block type = %v, want header", blocks[0]["type"])
	}
	actionBlock := blocks[len(blocks)-1]
	if actionBlock["type"] != "actions" {
		t.Fatalf("last block type = %v, want actions", actionBlock["type"])
	}
	elements, ok := actionBlock["elements"].([]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("elements = %#v", actionBlock["elements"])
	}
	btn, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("button = %#v", elements[0])
	}
	value, ok := btn["value"].(string)
	if !ok {
		t.Fatalf("button value = %#v", btn["value"])
	}
	action, sessionKey, _ := decodeActionValue(value)
	if action != "nav:/help session" || sessionKey != "slack:C1:U1" {
		t.Fatalf("decoded = %q %q", action, sessionKey)
	}
}

func TestRenderCardBlocks_EqualColumnsSplitRows(t *testing.T) {
	card := core.NewCard().ButtonsEqual(
		core.PrimaryBtn("A", "nav:/help session"),
		core.DefaultBtn("B", "nav:/help agent"),
		core.DefaultBtn("C", "nav:/help tools"),
		core.DefaultBtn("D", "nav:/help system"),
	).Build()

	blocks := decodeRenderedBlocks(t, card, "")
	actionCount := 0
	for _, block := range blocks {
		if block["type"] == "actions" {
			actionCount++
		}
	}
	if actionCount != 2 {
		t.Fatalf("action blocks = %d, want 2", actionCount)
	}
}

func TestRenderCardBlocks_ListItemAccessory(t *testing.T) {
	card := core.NewCard().
		ListItemBtn("session summary", "#1", "primary", "act:/switch 1").
		Build()

	blocks := decodeRenderedBlocks(t, card, "slack:C:U")
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v", blocks)
	}
	if blocks[0]["type"] != "section" {
		t.Fatalf("block type = %v", blocks[0]["type"])
	}
	accessory, ok := blocks[0]["accessory"].(map[string]any)
	if !ok || accessory["type"] != "button" {
		t.Fatalf("accessory = %#v", blocks[0]["accessory"])
	}
}

func TestTrackAndRefreshCardRequiresMessage(t *testing.T) {
	p := &Platform{}
	err := p.RefreshCard(t.Context(), "slack:C:U", core.NewCard().Markdown("x").Build())
	if err == nil {
		t.Fatal("expected error when no tracked message")
	}
}

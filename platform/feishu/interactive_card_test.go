package feishu

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
)

func TestInteractiveReceiptDoesNotPromiseSessionContinuation(t *testing.T) {
	for _, custom := range []bool{false, true} {
		v := &core.Decision{ReturnMode: "none", Status: "answered", OptionID: "ok", Spec: core.DecisionSpec{Title: "Review", Markdown: "Details", Options: []core.DecisionOption{{ID: "ok", Label: "OK"}}}}
		if custom {
			v.Spec.Card = json.RawMessage(`{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"Details"},{"tag":"button","name":"ok","text":{"tag":"plain_text","content":"OK"}}]}}`)
		}
		raw, _ := json.Marshal(decisionCard(v, true))
		if strings.Contains(string(raw), "original session") || strings.Contains(string(raw), `"tag":"button"`) {
			t.Fatal(string(raw))
		}
		if !strings.Contains(string(raw), "Interaction recorded") {
			t.Fatal(string(raw))
		}
		v.Status = "closed"
		raw, _ = json.Marshal(decisionCard(v, false))
		if !strings.Contains(string(raw), "Card closed") || strings.Contains(string(raw), `"tag":"button"`) {
			t.Fatal(string(raw))
		}
	}
}

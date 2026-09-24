package feishu

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
)

func TestDecisionCustomCardOwnsLayoutButNotRouting(t *testing.T) {
	s := core.DecisionSpec{Card: json.RawMessage(`{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"Build"},"template":"purple"},"body":{"elements":[{"tag":"markdown","content":"**Parameters**"},{"tag":"column_set","columns":[{"tag":"column","width":"weighted","weight":1,"elements":[{"tag":"input","name":"version","default_value":"1.0.0","required":true}]},{"tag":"column","width":"weighted","weight":1,"elements":[{"tag":"multi_select_static","name":"platforms","selected_values":["ipa"],"options":[{"text":{"tag":"plain_text","content":"IPA"},"value":"ipa"}]}]}]},{"tag":"button","name":"build","text":{"tag":"plain_text","content":"Start"}},{"tag":"button","name":"back","skip_validation":true,"text":{"tag":"plain_text","content":"Back"}}]}}`)}
	p := &Platform{}
	if err := p.PrepareDecisionSpec(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Fields) != 2 || len(s.Options) != 2 || !s.Options[1].SkipValidation {
		t.Fatalf("bad derived contract %+v", s)
	}
	v := &core.Decision{ID: "host-request", Revision: 4, Spec: s, Values: map[string]any{"version": "2.0.0", "platforms": []string{"ipa"}}, OptionID: "build"}
	raw, _ := json.Marshal(decisionCard(v, false))
	for _, want := range []string{"purple", "column_set", "host-request", `"revision":"4"`, `"selected_values":["ipa"]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("lost %s: %s", want, raw)
		}
	}
	receipt, _ := json.Marshal(decisionCard(v, true))
	if strings.Contains(string(receipt), `"tag":"button"`) || strings.Contains(string(receipt), `"tag":"input"`) || !strings.Contains(string(receipt), "2.0.0") || !strings.Contains(string(receipt), "purple") {
		t.Fatalf("bad receipt %s", receipt)
	}
}
func TestDecisionCustomCardRejectsForeignCallbacks(t *testing.T) {
	for _, card := range []string{
		`{"schema":"2.0","body":{"elements":[{"tag":"button","name":"x","behaviors":[{"type":"callback","value":{"request_id":"foreign"}}]}]}}`,
		`{"schema":"2.0","body":{"elements":[{"tag":"unknown"}]}}`,
		`{"schema":"2.0","body":{"elements":[{"tag":"form","elements":[{"tag":"form","elements":[]}]}]}}`,
	} {
		s := core.DecisionSpec{Card: json.RawMessage(card)}
		if (&Platform{}).PrepareDecisionSpec(&s) == nil {
			t.Fatal("unsafe card accepted")
		}
	}
}

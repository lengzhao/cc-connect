package feishu

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
)

func TestDecisionMultilineIndependentOfCardWidth(t *testing.T) {
	for _, width := range []string{"default", "compact", "fill"} {
		v := &core.Decision{Spec: core.DecisionSpec{Title: "Form", WidthMode: width, Fields: []core.DecisionField{{ID: "details", Label: "Details", Type: "textarea", Default: "first\nsecond", MaxLength: 1000}}, Options: []core.DecisionOption{{ID: "send", Label: "Send"}}}}
		b, _ := json.Marshal(decisionCard(v, false))
		if !strings.Contains(string(b), `"width_mode":"`+width+`"`) || !strings.Contains(string(b), `"input_type":"multiline_text"`) {
			t.Fatalf("bad multiline card: %s", b)
		}
		for _, tag := range []string{"input", "textarea"} {
			s := core.DecisionSpec{Card: json.RawMessage(`{"schema":"2.0","config":{"width_mode":"` + width + `"},"body":{"elements":[{"tag":"` + tag + `","input_type":"multiline_text","width":"default","rows":4,"auto_resize":true,"max_rows":8,"name":"details","default_value":"first\nsecond"},{"tag":"button","name":"send","text":{"tag":"plain_text","content":"Send"}}]}}`)}
			if err := (&Platform{}).PrepareDecisionSpec(&s); err != nil {
				t.Fatal(err)
			}
			if s.Fields[0].Type != "textarea" {
				t.Fatal("multiline not recognized")
			}
			s.Fields[0].MaxLength = 1000
			v.Spec = s
			v.Values = map[string]any{"details": "first\nsecond"}
			v.OptionID = "send"
			card := decisionCard(v, false)
			raw, _ := json.Marshal(card)
			for _, part := range []string{`"input_type":"multiline_text"`, `"width":"default"`, `"rows":4`, `"auto_resize":true`, `"width_mode":"` + width + `"`} {
				if !strings.Contains(string(raw), part) {
					t.Fatalf("lost %s: %s", part, raw)
				}
			}
			if strings.Contains(string(raw), `"tag":"textarea"`) {
				t.Fatal("non-native textarea sent")
			}
			receipt, _ := json.Marshal(decisionCard(v, true))
			if !strings.Contains(string(receipt), `first\nsecond`) || strings.Contains(string(receipt), `"tag":"input"`) {
				t.Fatalf("bad readonly multiline: %s", receipt)
			}
		}
	}
	v := &core.Decision{Spec: core.DecisionSpec{Title: "Default"}}
	if decisionCard(v, false)["config"].(map[string]any)["width_mode"] != "default" {
		t.Fatal("default card forced wide")
	}
}
func TestDecisionNativeComponentContract(t *testing.T) {
	for _, tc := range []struct{ tag, typ, extra string }{
		{"select_person", "person", `"initial_option":"ou_a","options":[{"value":"ou_a"}]`},
		{"multi_select_person", "people", `"selected_values":["ou_a"],"options":[{"value":"ou_a"}]`},
		{"date_picker", "date", `"initial_date":"2026-09-25"`},
		{"picker_time", "time", `"initial_time":"15:30"`},
		{"picker_datetime", "datetime", `"initial_datetime":"2026-09-25 15:30"`},
		{"select_img", "image", `"options":[{"img_key":"img_a","value":"a"}]`},
		{"select_img", "images", `"multi_select":true,"options":[{"img_key":"img_a","value":"a"}]`},
	} {
		s := core.DecisionSpec{Card: json.RawMessage(`{"schema":"2.0","body":{"elements":[{"tag":"` + tc.tag + `","name":"field",` + tc.extra + `},{"tag":"button","name":"submit","text":{"tag":"plain_text","content":"Submit"}}]}}`)}
		if err := (&Platform{}).PrepareDecisionSpec(&s); err != nil {
			t.Fatal(tc.tag, err)
		}
		if s.Fields[0].Type != tc.typ {
			t.Fatalf("%s mapped to %s", tc.tag, s.Fields[0].Type)
		}
		v := &core.Decision{Spec: s, Values: map[string]any{"field": "value"}}
		rendered, _ := json.Marshal(decisionCard(v, false))
		if !strings.Contains(string(rendered), `"tag":"`+tc.tag+`"`) {
			t.Fatalf("lost native component: %s", rendered)
		}
		receipt, _ := json.Marshal(decisionCard(v, true))
		if strings.Contains(string(receipt), `"tag":"`+tc.tag+`"`) {
			t.Fatalf("interactive receipt: %s", receipt)
		}
	}
}
func TestDecisionLayoutTableAndPanel(t *testing.T) {
	s := core.DecisionSpec{Card: json.RawMessage(`{"schema":"2.0","config":{"wide_screen_mode":false},"body":{"elements":[{"tag":"table","columns":[{"name":"a","data_type":"text"}],"rows":[{"a":"x"}]},{"tag":"collapsible_panel","header":{"title":{"tag":"plain_text","content":"Details"}},"elements":[{"tag":"textarea","name":"notes"}]},{"tag":"button","name":"submit","text":{"tag":"plain_text","content":"Submit"}}]}}`)}
	if err := (&Platform{}).PrepareDecisionSpec(&s); err != nil {
		t.Fatal(err)
	}
	card := decisionCard(&core.Decision{Spec: s}, false)
	nodes := card["body"].(map[string]any)["elements"].([]any)
	if nodes[0].(map[string]any)["tag"] != "table" || nodes[1].(map[string]any)["tag"] != "form" {
		t.Fatal("table incorrectly wrapped into form")
	}
	if card["config"].(map[string]any)["width_mode"] != "default" {
		t.Fatal("legacy narrow mode ignored")
	}
	for _, body := range []string{
		`{"tag":"collapsible_panel","elements":[{"tag":"form","elements":[]}]}`,
		`{"tag":"form","elements":[{"tag":"table","columns":[],"rows":[]}]}`,
		`{"tag":"input","name":"a"},{"tag":"table","columns":[],"rows":[]},{"tag":"button","name":"ok","text":{"content":"OK"}}`,
	} {
		bad := core.DecisionSpec{Card: json.RawMessage(`{"schema":"2.0","body":{"elements":[` + body + `]}}`)}
		if (&Platform{}).PrepareDecisionSpec(&bad) == nil {
			t.Fatalf("invalid nesting accepted: %s", body)
		}
	}
}

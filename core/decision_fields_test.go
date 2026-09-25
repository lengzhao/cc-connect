package core

import (
	"context"
	"testing"
)

func TestDecisionFormAtomicValidationPersistenceAndReplay(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	spec := decisionSpec()
	spec.Options = append(spec.Options, DecisionOption{ID: "cancel", Label: "Cancel", Cancel: true})
	spec.Fields = []DecisionField{{ID: "branch", Label: "Branch", Type: "text", Required: true, MaxLength: 50}, {ID: "env", Label: "Environment", Type: "select", Required: true, Options: []DecisionOption{{ID: "uat", Label: "UAT"}}}, {ID: "targets", Label: "Targets", Type: "multiselect", Options: []DecisionOption{{ID: "ipa", Label: "IPA"}, {ID: "apk", Label: "APK"}}}}
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID, map[string]any{"branch": "develop"}); err == nil {
		t.Fatal("accepted incomplete form")
	}
	values := map[string]any{"branch": "develop", "env": "uat", "targets": []any{"ipa", "apk"}}
	answer, err := p.handler(v.ID, "alice", "yes", "notes", v.MessageID, values)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Values["branch"] != "develop" {
		t.Fatalf("lost values: %+v", answer)
	}
	restored := newDecisionService(e, e.sessions.storePath)
	if restored.items[v.ID] == nil || !decisionValuesEqual(restored.items[v.ID].Values, answer.Values) {
		t.Fatal("persisted values lost after restart")
	}
	values["targets"] = []any{"apk", "ipa"}
	if _, err = p.handler(v.ID, "alice", "yes", "notes", v.MessageID, values); err != nil {
		t.Fatal("duplicate callback rejected", err)
	}
	values["branch"] = "main"
	if _, err = p.handler(v.ID, "alice", "yes", "notes", v.MessageID, values); err == nil {
		t.Fatal("changed submission accepted")
	}
}
func TestDecisionFormCancelSkipsRequiredFields(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	spec := decisionSpec()
	spec.Options = append(spec.Options, DecisionOption{ID: "cancel", Label: "Cancel", Cancel: true})
	spec.Fields = []DecisionField{{ID: "version", Label: "Version", Type: "text", Required: true}}
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.handler(v.ID, "alice", "cancel", "", v.MessageID)
	if err != nil || result.OptionID != "cancel" || result.Status != "answered" {
		t.Fatalf("cancel failed: %+v %v", result, err)
	}
}
func TestDecisionFieldValidation(t *testing.T) {
	for _, f := range []DecisionField{{ID: "comment", Label: "x", Type: "text"}, {ID: "x", Label: "x", Type: "unknown"}, {ID: "x", Label: "x", Type: "select", Options: []DecisionOption{{ID: "one", Label: "One"}}, Default: "invalid"}} {
		s := decisionSpec()
		s.Fields = []DecisionField{f}
		if validateDecision(&s) == nil {
			t.Fatalf("accepted invalid field %+v", f)
		}
	}
}

func TestDecisionCheckboxSubmissionAndPersistence(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	s := decisionSpec()
	s.Fields = []DecisionField{{ID: "consent", Label: "Consent", Type: "checkbox", Required: true}, {ID: "optional", Label: "Optional", Type: "checkbox", Default: true}}
	v, err := e.decisions.create(context.Background(), token, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []any{nil, false, "true", []any{true}} {
		if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID, map[string]any{"consent": raw}); err == nil {
			t.Fatalf("accepted invalid consent %#v", raw)
		}
	}
	answer, err := p.handler(v.ID, "alice", "yes", "", v.MessageID, map[string]any{"consent": true, "optional": false})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Values["consent"] != true || answer.Values["optional"] != false {
		t.Fatalf("lost boolean values: %+v", answer.Values)
	}
	restored := newDecisionService(e, e.sessions.storePath)
	if !decisionValuesEqual(restored.items[v.ID].Values, answer.Values) {
		t.Fatal("lost checkbox state on restart")
	}
	if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID, answer.Values); err != nil {
		t.Fatal(err)
	}
}
func TestDecisionCheckboxOptionalAndSkipValidation(t *testing.T) {
	s := decisionSpec()
	s.Fields = []DecisionField{{ID: "check", Label: "Check", Type: "checkbox", Required: true}}
	s.Options = append(s.Options, DecisionOption{ID: "back", Label: "Back", SkipValidation: true})
	v, err := validateDecisionValues(s, "back", nil)
	if err != nil || v["check"] != false {
		t.Fatalf("skip validation: %v %v", v, err)
	}
	s.Fields[0].Default = "true"
	if validateDecisionFields(&s) == nil {
		t.Fatal("accepted string default")
	}
}

func TestDecisionNativeValuesValidationAndPersistence(t *testing.T) {
	s := decisionSpec()
	s.WidthMode = "compact"
	s.Fields = []DecisionField{{ID: "notes", Label: "Notes", Type: "textarea"}, {ID: "date", Label: "Date", Type: "date"}, {ID: "time", Label: "Time", Type: "time"}, {ID: "when", Label: "When", Type: "datetime"}, {ID: "person", Label: "Person", Type: "person"}, {ID: "people", Label: "People", Type: "people"}, {ID: "image", Label: "Image", Type: "image", Options: []DecisionOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}}
	e, p, _, token := decisionFixture(t)
	v, err := e.decisions.create(context.Background(), token, s)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"notes": "line one\nline two", "date": "2026-09-25 +0800", "time": "15:30 +0800", "when": "2026-09-25 15:30 +0800", "person": "ou_a", "people": []any{"ou_b", "ou_a"}, "image": []any{"a"}}
	answer, err := p.handler(v.ID, "alice", "yes", "", v.MessageID, values)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Values["notes"] != "line one\nline two" {
		t.Fatal("lost newline")
	}
	restored := newDecisionService(e, e.sessions.storePath)
	if !decisionValuesEqual(answer.Values, restored.items[v.ID].Values) {
		t.Fatal("lost values on restart")
	}
	values["people"] = []any{"ou_a", "ou_b"}
	if _, err = p.handler(v.ID, "alice", "yes", "", v.MessageID, values); err != nil {
		t.Fatal("same people should be idempotent", err)
	}
	for _, tc := range []struct {
		f   DecisionField
		raw any
	}{
		{DecisionField{Type: "date"}, "2026-02-30"}, {DecisionField{Type: "time"}, "25:00"},
		{DecisionField{Type: "person", Options: []DecisionOption{{ID: "ou_a"}}}, "ou_b"},
		{DecisionField{Type: "people"}, []any{"ou_a", "ou_a"}},
		{DecisionField{Type: "image", Options: []DecisionOption{{ID: "a"}, {ID: "b"}}}, []any{"a", "b"}},
	} {
		if _, err := normalizeDecisionValue(tc.f, tc.raw, false); err == nil {
			t.Fatalf("accepted invalid %s: %v", tc.f.Type, tc.raw)
		}
	}
}

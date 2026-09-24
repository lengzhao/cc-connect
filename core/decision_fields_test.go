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

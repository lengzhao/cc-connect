package core

import (
	"strings"
	"testing"
)

func TestSanitizeAgentContext_DropsInvalidCustomKeys(t *testing.T) {
	in := AgentContext{
		Language: "zh",
		TaskID:   "task-1",
		TraceID:  "trace-1",
		Custom: map[string]string{
			"custom.tenant_id": "acme",
			"tenant_id":        "bad",
			"custom.":          "bad",
			"custom.X":         "bad",
		},
	}
	out := SanitizeAgentContext(in)
	if out.Language != "zh" || out.TaskID != "task-1" || out.TraceID != "trace-1" {
		t.Fatalf("standard fields not preserved: %+v", out)
	}
	if len(out.Custom) != 1 || out.Custom["custom.tenant_id"] != "acme" {
		t.Fatalf("custom map = %#v, want only custom.tenant_id=acme", out.Custom)
	}
}

func TestSanitizeAgentContext_EscapesAndTruncatesValues(t *testing.T) {
	long := strings.Repeat("a", maxAgentContextValueLen+20)
	in := AgentContext{
		Language: "en\nUS",
		TaskID:   `ab"cd`,
		Custom:   map[string]string{"custom.note": "line1\nline2"},
	}
	out := SanitizeAgentContext(in)
	if out.Language != "" {
		t.Fatalf("invalid language should be dropped, got %q", out.Language)
	}
	if out.TaskID != "abcd" {
		t.Fatalf("task_id = %q, want abcd after quote strip", out.TaskID)
	}
	if out.Custom["custom.note"] != "line1 line2" {
		t.Fatalf("custom value = %q, want newlines collapsed", out.Custom["custom.note"])
	}

	in2 := AgentContext{TaskID: long}
	out2 := SanitizeAgentContext(in2)
	if len(out2.TaskID) != maxAgentContextValueLen {
		t.Fatalf("task_id len = %d, want %d", len(out2.TaskID), maxAgentContextValueLen)
	}
}

func TestFilterAgentContextByAllowlist(t *testing.T) {
	in := AgentContext{
		Language: "ja",
		TaskID:   "t1",
		TraceID:  "tr1",
		Custom: map[string]string{
			"custom.tenant_id": "acme",
			"custom.region":    "cn",
		},
	}

	none := FilterAgentContextByAllowlist(in, nil)
	if !none.Empty() {
		t.Fatalf("nil allowlist should drop all: %+v", none)
	}

	stdOnly := FilterAgentContextByAllowlist(in, []string{"language", "task_id"})
	if stdOnly.Language != "ja" || stdOnly.TaskID != "t1" || stdOnly.TraceID != "" || len(stdOnly.Custom) != 0 {
		t.Fatalf("stdOnly = %+v", stdOnly)
	}

	oneCustom := FilterAgentContextByAllowlist(in, []string{"custom.tenant_id"})
	if len(oneCustom.Custom) != 1 || oneCustom.Custom["custom.tenant_id"] != "acme" {
		t.Fatalf("oneCustom = %+v", oneCustom)
	}

	allCustom := FilterAgentContextByAllowlist(in, []string{"custom.*"})
	if len(allCustom.Custom) != 2 {
		t.Fatalf("custom.* should keep all custom keys, got %#v", allCustom.Custom)
	}
}

func TestAgentContextPromptAttrs_StableOrder(t *testing.T) {
	ctx := AgentContext{
		Language: "zh",
		TaskID:   "t-9",
		TraceID:  "tr-1",
		Custom: map[string]string{
			"custom.zulu":      "z",
			"custom.alpha":     "a",
			"custom.tenant_id": "acme",
		},
	}
	attrs := ctx.PromptAttrs()
	want := []string{
		`language="zh"`,
		`task_id="t-9"`,
		`trace_id="tr-1"`,
		`custom.alpha="a"`,
		`custom.tenant_id="acme"`,
		`custom.zulu="z"`,
	}
	if len(attrs) != len(want) {
		t.Fatalf("attrs = %#v, want %#v", attrs, want)
	}
	for i := range want {
		if attrs[i] != want[i] {
			t.Fatalf("attrs[%d] = %q, want %q (full=%#v)", i, attrs[i], want[i], attrs)
		}
	}
}

func TestNormalizeInjectContextAllowlist(t *testing.T) {
	got := NormalizeInjectContextAllowlist([]string{
		" Language ",
		"TASK_ID",
		"custom.*",
		"custom.tenant_id",
		"unknown",
		"custom.Bad",
		"",
	})
	want := []string{"language", "task_id", "custom.*", "custom.tenant_id"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

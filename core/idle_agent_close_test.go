package core

import "testing"

func TestCloseIdleAgentSessionsResult_Fields(t *testing.T) {
	r := CloseIdleAgentSessionsResult{
		Closed:             1,
		Skipped:            2,
		ClosedSessionKeys:  []string{"a"},
		SkippedSessionKeys: []string{"b", "c"},
	}
	if r.Closed != 1 || r.Skipped != 2 {
		t.Fatalf("unexpected counts: %+v", r)
	}
	if len(r.ClosedSessionKeys) != 1 || len(r.SkippedSessionKeys) != 2 {
		t.Fatalf("unexpected keys: %+v", r)
	}
}

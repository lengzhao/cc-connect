package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSharedCardStoreSurvivesLostWorkspaceAndRunsManagementWithoutReply(t *testing.T) {
	var mu sync.Mutex
	snapshots := map[string]json.RawMessage{}
	var command map[string]any
	resultDone := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/card-commands/") {
			if r.Method == http.MethodPut {
				resultDone = true
				command = nil
				json.NewEncoder(w).Encode(map[string]bool{"ok": true})
				return
			}
			items := []any{}
			if command != nil {
				items = append(items, map[string]any{"command_id": "cmd1", "request": command})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		}
		if r.Method == http.MethodGet {
			items := []json.RawMessage{}
			for _, v := range snapshots {
				items = append(items, v)
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		}
		var req struct {
			Expected int64           `json:"expected_store_revision"`
			Snapshot json.RawMessage `json:"snapshot"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var v Decision
		json.Unmarshal(req.Snapshot, &v)
		var old Decision
		json.Unmarshal(snapshots[v.ID], &old)
		if old.StoreRevision != req.Expected {
			http.Error(w, "conflict", 409)
			return
		}
		snapshots[v.ID] = req.Snapshot
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()
	t.Setenv("CC_CARD_STORE_URL", server.URL)
	t.Setenv("CC_CARD_STORE_TOKEN", "fixture")
	e, p, session, token := decisionFixture(t)
	v, err := e.decisions.manage(context.Background(), interactiveCardRequest{Token: token, Action: "create", Spec: decisionSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(v.ID, "alice", "yes", "approved", v.MessageID); err != nil {
		t.Fatal(err)
	}
	// Rehydrate using an entirely different local path.
	e.decisions = newDecisionService(e, t.TempDir()+"/empty-sessions")
	got, err := e.decisions.managedCard(v.ID)
	if err != nil || len(got.Interactions) != 1 {
		t.Fatal("remote restore failed", err)
	}
	mu.Lock()
	command = map[string]any{"action": "update", "card_id": v.ID, "expected_revision": 1, "actor": "trainer", "spec": decisionSpec()}
	mu.Unlock()
	e.decisions.drainOne()
	e.decisions.pollCardCommands()
	got, err = e.decisions.managedCard(v.ID)
	if err != nil || got.Revision != 2 || got.ReturnMode != "none" {
		t.Fatal("management failed", err)
	}
	mu.Lock()
	done := resultDone
	mu.Unlock()
	if !done {
		t.Fatal("command result not acknowledged")
	}
	if len(got.Changes) != 1 || got.Changes[0].Actor != "lts-service:trainer" {
		t.Fatal("missing management attribution")
	}
	for _, h := range session.GetHistory(0) {
		if strings.Contains(h.Content, v.ID) {
			t.Fatal("management woke session")
		}
	}
}

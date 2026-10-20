package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCardOwnerAliasAskUserAndInteractiveLifecycle(t *testing.T) {
	var mu sync.Mutex
	snapshots := map[string]json.RawMessage{}
	wrongOwner := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture" || !strings.HasPrefix(r.URL.Path, "/api/card-store/ambrefeedback") {
			http.Error(w, "wrong authorized namespace", 401)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/card-store/ambrefeedback")
		id = strings.TrimPrefix(id, "/")
		if r.Method == http.MethodGet {
			items := []json.RawMessage{}
			for key, raw := range snapshots {
				if id == "" || key == id {
					if wrongOwner {
						var v Decision
						_ = json.Unmarshal(raw, &v)
						v.OwnerAutomon = "another-automon"
						raw, _ = json.Marshal(v)
					}
					items = append(items, raw)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		}
		var req struct {
			Expected int64           `json:"expected_store_revision"`
			Snapshot json.RawMessage `json:"snapshot"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad payload", 400)
			return
		}
		var v, old Decision
		_ = json.Unmarshal(req.Snapshot, &v)
		_ = json.Unmarshal(snapshots[id], &old)
		if v.OwnerAutomon != "ambrefeedback" || v.ID != id {
			http.Error(w, "invalid snapshot owner", 400)
			return
		}
		if old.StoreRevision != req.Expected || v.StoreRevision != req.Expected+1 {
			http.Error(w, "CAS conflict", 409)
			return
		}
		snapshots[id] = append(json.RawMessage(nil), req.Snapshot...)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()
	t.Setenv("CC_CARD_STORE_URL", server.URL)
	t.Setenv("CC_CARD_STORE_TOKEN", "fixture")
	t.Setenv("CC_CARD_STORE_OWNER_ALIASES", `{"628":"ambrefeedback"}`)
	fixture := func() (*Engine, *decisionTestPlatform, string) {
		p := &decisionTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
		e := NewEngine("628", &cujAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
		t.Cleanup(e.cancel)
		session := e.sessions.GetOrCreateActive("test:room:thread:root")
		m := &Message{SessionKey: "test:room:thread:root", Platform: "test", UserID: "alice", UserEmail: "alice@example.com", MessageID: "origin"}
		e.decisions.remember(p, m, session, e.sessions, m.SessionKey, "")
		return e, p, e.decisions.tokenFor(m.SessionKey, session.ID)
	}
	e, p, token := fixture()
	ask, err := e.decisions.create(context.Background(), token, decisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	if ask.OwnerAutomon != "628" || ask.ReturnMode != "session" {
		t.Fatalf("wrong local ask ownership: %+v", ask)
	}
	if _, err = p.handler(ask.ID, "alice", "yes", "approved", ask.MessageID); err != nil {
		t.Fatal(err)
	}
	card, err := e.decisions.manage(context.Background(), interactiveCardRequest{Token: token, Action: "create", CardID: "external-release-123", Spec: decisionSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.handler(card.ID, "alice", "yes", "read", card.MessageID); err != nil {
		t.Fatal(err)
	}
	// Restart with empty local state: old code-owned snapshots must rehydrate as
	// this project's ID without changing revisions, receipt history or return mode.
	other, otherPlatform, otherToken := fixture()
	gotAsk, err := other.decisions.managedCard(ask.ID)
	if err != nil || gotAsk.OwnerAutomon != "628" || gotAsk.OptionID != "yes" {
		t.Fatalf("ask restore: %+v %v", gotAsk, err)
	}
	got, err := other.decisions.manage(context.Background(), interactiveCardRequest{Token: otherToken, Action: "get", CardID: card.ID})
	if err != nil || len(got.Interactions) != 1 || got.ReturnMode != "none" {
		t.Fatalf("notification restore: %+v %v", got, err)
	}
	replay, err := other.decisions.manage(context.Background(), interactiveCardRequest{Token: otherToken, Action: "create", CardID: card.ID, Spec: decisionSpec()})
	if err != nil || replay.ID != card.ID || len(otherPlatform.cards) != 0 {
		t.Fatalf("idempotent replay: %+v %v", replay, err)
	}
	revision := got.Revision
	updated, err := other.decisions.manage(context.Background(), interactiveCardRequest{Token: otherToken, Action: "update", CardID: card.ID, ExpectedRevision: &revision, Spec: decisionSpec()})
	if err != nil || updated.Revision != revision+1 || len(updated.Interactions) != 1 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	revision = updated.Revision
	closed, err := other.decisions.manage(context.Background(), interactiveCardRequest{Token: otherToken, Action: "close", CardID: card.ID, ExpectedRevision: &revision})
	if err != nil || closed.Status != "closed" {
		t.Fatalf("close: %+v %v", closed, err)
	}
	// Remote responses do not grant aliases. A mismatching owner still fails.
	mu.Lock()
	wrongOwner = true
	mu.Unlock()
	if _, err = other.decisions.managedCard(card.ID); err == nil {
		t.Fatal("accepted foreign owner")
	}
	if bad, _, _ := fixture(); bad.decisions.loadErr == nil {
		t.Fatal("accepted foreign owner during startup")
	}
}

func TestCardStoreOwnerAliasesFailClosed(t *testing.T) {
	t.Setenv("CC_CARD_STORE_URL", "http://fixture.invalid")
	t.Setenv("CC_CARD_STORE_TOKEN", "fixture")
	for _, raw := range []string{`{`, `{"628":""}`, `{"628":"../other"}`} {
		t.Setenv("CC_CARD_STORE_OWNER_ALIASES", raw)
		if _, err := newCardStore("628"); err == nil {
			t.Fatalf("accepted invalid mapping %s", raw)
		}
	}
	t.Setenv("CC_CARD_STORE_OWNER_ALIASES", `{"628":"ambrefeedback"}`)
	store, err := newCardStore("another-project")
	if err != nil || store.owner != "another-project" {
		t.Fatalf("mapping leaked to unrelated project: %+v %v", store, err)
	}
}

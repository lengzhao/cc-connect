package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type locationTestPlatform struct {
	*decisionTestPlatform
	lookup func(string) (DecisionMessageLocation, error)
}

func (p *locationTestPlatform) LocateDecisionMessage(_ context.Context, id string) (DecisionMessageLocation, error) {
	return p.lookup(id)
}

func TestAskUserMessageLocationSurvivesCallbackAndRestart(t *testing.T) {
	e, p, session, token := decisionFixture(t)
	thread := ""
	lp := &locationTestPlatform{p, func(id string) (DecisionMessageLocation, error) {
		return DecisionMessageLocation{Platform: "test", MessageID: id, ChatID: "group", ThreadID: thread, RootID: "origin-message"}, nil
	}}
	e.platforms = []Platform{lp}
	// Exercise the actual ask_user HTTP response, not just the helper DTO.
	server := &APIServer{engines: map[string]*Engine{"proj": e}}
	body, _ := json.Marshal(map[string]any{"project": "proj", "token": token, "spec": decisionSpec()})
	w := httptest.NewRecorder()
	server.handleAskUser(w, httptest.NewRequest("POST", "/ask-user", strings.NewReader(string(body))))
	var response struct {
		RequestID      string                  `json:"request_id"`
		MessageContext *DecisionMessageContext `json:"message_context"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.MessageContext == nil {
		t.Fatalf("missing location in ask_user response: %s", w.Body.String())
	}
	if response.MessageContext.Origin.MessageID != "origin-message" || response.MessageContext.Card.MessageID != "card-"+response.RequestID || !response.MessageContext.DeliveryIsOriginConversation {
		t.Fatalf("wrong locations: %+v", response.MessageContext)
	}
	// First group message had no topic; the reply creates one before the click.
	thread = "actual-thread"
	v := e.decisions.items[response.RequestID]
	if _, err := p.handler(v.ID, "alice", "yes", `{"message_context":"forged","thread_id":"forged-thread"}`, v.MessageID); err != nil {
		t.Fatal(err)
	}
	d := newDecisionService(e, strings.TrimSuffix(e.decisions.path, ".decisions"))
	e.decisions = d
	d.drainOne()
	waitDecision(t, d, v.ID, "delivered")
	deadline := time.Now().Add(2 * time.Second)
	for session.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	found := false
	for _, h := range session.GetHistory(0) {
		if h.Role == "user" && strings.Contains(h.Content, "[ask_user message_context") {
			trusted := strings.Split(h.Content, "[ask_user result")[0]
			if !strings.Contains(trusted, "actual-thread") || strings.Contains(trusted, "forged") || !strings.Contains(trusted, "card-"+v.ID) {
				t.Fatalf("bad continuation metadata: %s", trusted)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("callback lost host-provided message locations")
	}
	reloaded := newDecisionService(e, strings.TrimSuffix(d.path, ".decisions"))
	if got := reloaded.items[v.ID].MessageContext; got == nil || got.Origin.ThreadID != thread || got.Card.ThreadID != thread {
		t.Fatalf("location not persisted: %+v", got)
	}
}

func TestAskUserMessageLocationSeparatesDirectDeliveryAndLookupFailure(t *testing.T) {
	e, p, _, token := decisionFixture(t)
	e.platforms = []Platform{&locationTestPlatform{p, func(id string) (DecisionMessageLocation, error) {
		location := DecisionMessageLocation{Platform: "test", MessageID: id, ChatID: "direct"}
		if id == "origin-message" {
			location.ChatID, location.ThreadID = "group", "origin-thread"
		}
		return location, nil
	}}}
	spec := decisionSpec()
	spec.Recipient = "bob"
	v, err := e.decisions.create(context.Background(), token, spec)
	if err != nil {
		t.Fatal(err)
	}
	m := v.MessageContext
	if m.DeliveryIsOriginConversation || m.Card.ChatID != "direct" || m.Card.ThreadID != "" || m.Origin.ThreadID != "origin-thread" {
		t.Fatalf("direct card inherited origin topic: %+v", m)
	}
	e.platforms = []Platform{&locationTestPlatform{p, func(id string) (DecisionMessageLocation, error) {
		return DecisionMessageLocation{}, errors.New("secret API body")
	}}}
	m = e.decisions.resolveMessageContext(context.Background(), v)
	encoded, _ := json.Marshal(m)
	if m.Card.Status != "unavailable" || strings.Contains(string(encoded), "secret") || m.Card.ThreadID != "" {
		t.Fatalf("unsafe failure metadata: %s", encoded)
	}
	e.platforms = []Platform{&locationTestPlatform{p, func(id string) (DecisionMessageLocation, error) {
		return DecisionMessageLocation{Platform: "test", MessageID: "other", ChatID: "group", ThreadID: "wrong"}, nil
	}}}
	if got := e.decisions.resolveMessageContext(context.Background(), v); got.Card.Reason != "message_identity_mismatch" || got.Card.ThreadID != "" {
		t.Fatalf("accepted wrong message: %+v", got)
	}
}

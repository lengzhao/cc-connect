package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Card management authenticates a current caller, but does not bind ownership
// to that caller's conversation. Ownership is the configured engine/project.
func (d *decisionService) managementOrigin(token string) (DecisionOrigin, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range d.origins {
		if token != "" && b.token == token {
			return b.origin, nil
		}
	}
	return DecisionOrigin{}, fmt.Errorf("authenticated Runtime context required")
}

func (d *decisionService) managedCard(id string) (*Decision, error) {
	if d.store != nil {
		var remote struct {
			Items []*Decision `json:"items"`
		}
		if err := d.store.request(context.Background(), http.MethodGet, d.store.path(id), nil, &remote); err != nil {
			return nil, err
		}
		d.mu.Lock()
		for _, v := range remote.Items {
			if v.ID == id && v.OwnerAutomon == d.engine.name {
				current := d.items[id]
				if current == nil {
					d.items[id] = v
				} else if v.StoreRevision > current.StoreRevision {
					*current = *v
				}
			}
		}
		d.mu.Unlock()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loadErr != nil {
		return nil, d.loadErr
	}
	v := d.items[id]
	if v == nil {
		return nil, ErrDecisionNotFound
	}
	if v.OwnerAutomon != d.engine.name {
		return nil, fmt.Errorf("card ownership is not established for cross-session access")
	}
	// Detach nested fields/maps: callers must never mutate the stored snapshot.
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var copy Decision
	err = json.Unmarshal(data, &copy)
	return &copy, err
}

type interactiveCardRequest struct {
	Actor            string       `json:"actor,omitempty"`
	Project          string       `json:"project"`
	Token            string       `json:"token"`
	Action           string       `json:"action"`
	CardID           string       `json:"card_id,omitempty"`
	ExpectedRevision *int         `json:"expected_revision,omitempty"`
	Spec             DecisionSpec `json:"spec,omitempty"`
}

func (d *decisionService) manage(ctx context.Context, req interactiveCardRequest) (*Decision, error) {
	origin, err := d.managementOrigin(req.Token)
	if err != nil {
		return nil, err
	}
	return d.manageTrusted(ctx, req, origin, false)
}
func (d *decisionService) manageTrusted(ctx context.Context, req interactiveCardRequest, origin DecisionOrigin, service bool) (*Decision, error) {
	if service {
		origin.UserID = "lts-service:" + req.Actor
	}
	if service && req.Action != "update" && req.Action != "close" {
		return nil, fmt.Errorf("unsupported service card operation")
	}
	if req.Action == "create" {
		if req.CardID != "" || req.Spec.RequestID != "" || req.ExpectedRevision != nil {
			return nil, fmt.Errorf("create must omit card_id and revision")
		}
		return d.createMode(ctx, req.Token, req.Spec, "none")
	}
	v, err := d.managedCard(req.CardID)
	if err != nil {
		return nil, err
	}
	switch req.Action {
	case "get":
		return v, nil
	case "close":
		if req.ExpectedRevision == nil {
			return nil, fmt.Errorf("expected_revision is required")
		}
		return d.closeCard(ctx, v.ID, *req.ExpectedRevision, origin)
	case "update":
		if req.ExpectedRevision == nil {
			return nil, fmt.Errorf("expected_revision is required")
		}
		req.Spec.RequestID, req.Spec.ExpectedRevision = v.ID, *req.ExpectedRevision
		if prep, ok := d.engine.platformForName(v.Platform).(DecisionSpecPreparer); ok {
			if err := prep.PrepareDecisionSpec(&req.Spec); err != nil {
				return nil, err
			}
		}
		if err := validateDecision(&req.Spec); err != nil {
			return nil, err
		}
		return d.updateManaged(ctx, origin, req.Spec)
	default:
		return nil, fmt.Errorf("action must be create, get, update or close")
	}
}

func (s *APIServer) handleInteractiveCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req interactiveCardRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid card request", 400)
		return
	}
	s.mu.RLock()
	e := s.engines[req.Project]
	s.mu.RUnlock()
	if e == nil {
		http.Error(w, "project not found", 404)
		return
	}
	if e.decisions.store == nil {
		http.Error(w, "shared card store is not configured", 503)
		return
	}
	v, err := e.decisions.manage(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	apiJSON(w, 200, map[string]any{"card_id": v.ID, "request_id": v.ID, "revision": v.Revision, "card": v})
}

// Closing is durable before the external PATCH. A failed PATCH may leave stale
// buttons visible, but callbacks are rejected; the same close can be retried.
func (d *decisionService) closeCard(ctx context.Context, id string, revision int, origin DecisionOrigin) (*Decision, error) {
	d.mu.Lock()
	v := d.items[id]
	if v == nil || v.OwnerAutomon != d.engine.name {
		d.mu.Unlock()
		return nil, ErrDecisionNotFound
	}
	if v.Status != "closed" && v.Revision != revision {
		d.mu.Unlock()
		return nil, fmt.Errorf("card revision changed")
	}
	if v.Status == "updating" || v.Status == "recorded" || v.Status == "dispatching" {
		d.mu.Unlock()
		return nil, fmt.Errorf("card operation in progress")
	}
	if v.Status == "closed" && v.Revision != revision+1 {
		d.mu.Unlock()
		return nil, fmt.Errorf("card revision changed")
	}
	if v.Status != "closed" {
		old := *v
		v.Revision++
		v.Changes = append(append([]DecisionChange(nil), v.Changes...), DecisionChange{Revision: v.Revision, Action: "close", Actor: origin.UserID, SessionID: origin.SessionID, At: time.Now().UTC()})
		v.Status = "closed"
		if err := d.persistLocked(v); err != nil {
			*v = old
			d.mu.Unlock()
			return nil, err
		}
	}
	copy := *v
	d.mu.Unlock()
	p, ok := d.engine.platformForName(copy.Platform).(DecisionUpdater)
	if !ok {
		return nil, fmt.Errorf("platform cannot update cards")
	}
	err := p.UpdateDecision(ctx, &copy)
	d.mu.Lock()
	defer d.mu.Unlock()
	v = d.items[id]
	v.Error = ""
	if err != nil {
		v.Error = "Card closed; display update failed"
	}
	if saveErr := d.persistLocked(v); saveErr != nil {
		return nil, saveErr
	}
	copy = *v
	return &copy, err
}

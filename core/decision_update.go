package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type DecisionSpecPreparer interface{ PrepareDecisionSpec(*DecisionSpec) error }
type DecisionReceiptWriter interface {
	RecordDecisionReceipt(context.Context, *Decision) error
}

func decisionWaitingStatus(s DecisionSpec) string {
	if len(s.Options) == 0 {
		return "displayed"
	}
	return "pending"
}

type DecisionUpdater interface {
	UpdateDecision(context.Context, *Decision) error
}

func decisionTurnID(v *Decision) string {
	if v.Revision == 0 {
		return "decision:" + v.ID
	}
	return fmt.Sprintf("decision:%s:%d", v.ID, v.Revision)
}

// Updates stay bound to the original authenticated session and recipient. A
// durable revision fences old callbacks; uncertain PATCHes are retried only
// with the exact same spec and expected revision.
func (d *decisionService) update(ctx context.Context, origin DecisionOrigin, spec DecisionSpec) (*Decision, error) {
	raw, _ := json.Marshal(spec)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	d.mu.Lock()
	v := d.items[spec.RequestID]
	if v == nil {
		d.mu.Unlock()
		return nil, ErrDecisionNotFound
	}
	if origin.SessionID != v.Origin.SessionID || origin.SessionKey != v.Origin.SessionKey || origin.Workspace != v.Origin.Workspace || origin.UserID != v.Origin.UserID || origin.Platform != v.Origin.Platform || (v.Origin.AgentSessionID != "" && origin.AgentSessionID != v.Origin.AgentSessionID) {
		d.mu.Unlock()
		return nil, fmt.Errorf("only the original active conversation can update this card")
	}
	if spec.Recipient != "" && spec.Recipient != v.Spec.Recipient {
		d.mu.Unlock()
		return nil, fmt.Errorf("card recipient cannot change")
	}
	if time.Now().After(v.ExpiresAt) {
		d.mu.Unlock()
		return nil, fmt.Errorf("request expired")
	}
	p, ok := d.engine.platformForName(v.Platform).(DecisionUpdater)
	if !ok {
		d.mu.Unlock()
		return nil, fmt.Errorf("platform cannot update decision cards")
	}
	repeat := v.UpdateHash == hash && v.UpdateFrom == spec.ExpectedRevision && v.Revision == spec.ExpectedRevision+1
	if repeat && (v.Status == "pending" || v.Status == "displayed") {
		copy := *v
		d.mu.Unlock()
		return &copy, nil
	}
	if v.Status == "updating" {
		d.mu.Unlock()
		return nil, fmt.Errorf("card update in progress; retry the same update")
	}
	if !repeat {
		if v.Revision != spec.ExpectedRevision {
			d.mu.Unlock()
			return nil, fmt.Errorf("card revision changed")
		}
		if v.Status != "dispatching" && v.Status != "delivered" && v.Status != "delivery_unknown" && v.Status != "pending" && v.Status != "displayed" {
			d.mu.Unlock()
			return nil, fmt.Errorf("card is still recording an interaction; retry after continuation")
		}
		old := *v
		// Carry submitted values into fields unless the Agent deliberately provides
		// a new default. Invalid old choices are not copied to changed option sets.
		spec.Fields = append([]DecisionField(nil), spec.Fields...)
		for n := range spec.Fields {
			f := &spec.Fields[n]
			if f.Default == nil {
				if value, exists := v.Values[f.ID]; exists {
					if _, err := normalizeDecisionValue(*f, value, false); err == nil {
						f.Default = value
					}
				}
			}
		}
		spec.Recipient = v.Spec.Recipient
		v.Spec = spec
		v.Revision++
		v.UpdateFrom = spec.ExpectedRevision
		v.UpdateHash = hash
		v.Status = "updating"
		v.Error = ""
		if err := d.persistLocked(v); err != nil {
			*v = old
			d.mu.Unlock()
			return nil, err
		}
	} else {
		if v.Status != "update_unknown" {
			d.mu.Unlock()
			return nil, fmt.Errorf("update is no longer retryable")
		}
		v.Status = "updating"
		if err := d.persistLocked(v); err != nil {
			v.Status = "update_unknown"
			d.mu.Unlock()
			return nil, err
		}
	}
	copy := *v
	d.mu.Unlock()
	err := p.UpdateDecision(ctx, &copy)
	d.mu.Lock()
	defer d.mu.Unlock()
	v = d.items[copy.ID]
	// A user can click the newly patched card before the HTTP response returns.
	// Their authenticated callback is stronger evidence than the PATCH response.
	if v.Status != "updating" {
		result := *v
		return &result, nil
	}
	v.Status = decisionWaitingStatus(v.Spec)
	v.Error = ""
	if err != nil {
		v.Status = "update_unknown"
		v.Error = err.Error()
	}
	if saveErr := d.persistLocked(v); saveErr != nil {
		v.Status = "update_unknown"
		return nil, saveErr
	}
	result := *v
	if err != nil {
		return &result, fmt.Errorf("card update uncertain; retry identical request_id and expected_revision: %w", err)
	}
	return &result, nil
}

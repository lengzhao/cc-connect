package core

import (
	"context"
	"log/slog"
	"time"
)

// Receipt repair finishes before continuation may update the card revision.
// Attempts are persisted before I/O, so restarts cannot create an infinite retry.
func (d *decisionService) retryReceipt(item *Decision) {
	d.mu.Lock()
	v := d.items[item.ID]
	if v == nil || v.Status != "recorded" || v.Revision != item.Revision || time.Now().Before(v.ReceiptNextAt) {
		d.mu.Unlock()
		return
	}
	if v.ReceiptAttempts >= 3 {
		v.Status = "answered"
		v.ReceiptState = "failed"
		v.Error = "Interaction saved; card display update failed after 3 attempts"
		if err := d.persistLocked(v); err != nil {
			v.Status = "recorded"
			slog.Error("save receipt exhaustion", "error", err)
		}
		d.mu.Unlock()
		return
	}
	old := *v
	v.ReceiptAttempts++
	v.ReceiptNextAt = time.Now().Add(10 * time.Second)
	if err := d.persistLocked(v); err != nil {
		*v = old
		d.mu.Unlock()
		return
	}
	copy := *v
	writer, ok := d.engine.platformForName(v.Platform).(DecisionReceiptWriter)
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(d.engine.ctx, 5*time.Second)
	var err error
	if ok {
		err = writer.RecordDecisionReceipt(ctx, &copy)
	}
	cancel()
	d.mu.Lock()
	defer d.mu.Unlock()
	v = d.items[item.ID]
	if v == nil || v.Status != "recorded" || v.Revision != copy.Revision {
		return
	}
	old = *v
	if ok && err == nil {
		v.Status = "answered"
		v.ReceiptState = "updated"
		v.Error = ""
	} else {
		v.Error = "Interaction saved; receipt retry failed"
		slog.Warn("decision receipt retry failed", "request_id", v.ID, "revision", v.Revision, "attempt", v.ReceiptAttempts, "error", err)
		if v.ReceiptAttempts >= 3 {
			v.Status = "answered"
			v.ReceiptState = "failed"
		}
	}
	if err := d.persistLocked(v); err != nil {
		*v = old
		slog.Error("save receipt repair", "error", err)
	}
}

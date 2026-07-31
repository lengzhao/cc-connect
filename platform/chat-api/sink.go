package chatapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// runEventSink is the live SSE connection or a detached virtual sink.
type runEventSink interface {
	Event(name string, payload any) error
	Active() bool
}

type sseEventSink struct {
	w *sseWriter
}

func (s *sseEventSink) Event(name string, payload any) error {
	if s == nil || s.w == nil {
		return nil
	}
	return s.w.Event(name, payload)
}

func (s *sseEventSink) Active() bool { return s != nil && s.w != nil }

type detachedEventSink struct {
	run *runState
	p   *Platform
}

func (d *detachedEventSink) Active() bool { return false }

func (d *detachedEventSink) Event(name string, payload any) error {
	if d == nil || d.run == nil {
		return nil
	}
	if isRecoverableSSEEvent(name) {
		d.run.setLastRecoverable(name, payload)
	}
	if name == "question_request" && d.p != nil {
		d.p.notifyQuestionAsync(d.run)
	}
	return nil
}

func isRecoverableSSEEvent(name string) bool {
	switch name {
	case "text_delta", "thinking_delta", "question_request", "permission_request":
		return true
	default:
		return false
	}
}

type recoverableEvent struct {
	name      string
	payload   any
	createdAt time.Time
}

func (r *runState) setLastRecoverable(name string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Unresolved interaction must not be overwritten by later content deltas.
	if name == "text_delta" || name == "thinking_delta" {
		if ev := r.lastRecoverableEvent; ev != nil &&
			(ev.name == "question_request" || ev.name == "permission_request") {
			if r.interaction != nil && !r.interaction.Responded && !r.interaction.Expired {
				return
			}
		}
	}
	r.lastRecoverableEvent = &recoverableEvent{
		name:      name,
		payload:   payload,
		createdAt: time.Now(),
	}
}

func (r *runState) peekLastRecoverable() *recoverableEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRecoverableEvent
}

func (r *runState) clearLastRecoverable() {
	r.mu.Lock()
	r.lastRecoverableEvent = nil
	r.mu.Unlock()
}

func questionNotifyBody(run *runState) map[string]string {
	if run == nil {
		return nil
	}
	return map[string]string{
		"conversation_id": run.conversationID,
		"message_id":      run.messageID,
		"run_id":          run.id,
		"user_id":         run.user,
		"channel":         run.channelKey,
	}
}

func (p *Platform) notifyQuestionAsync(run *runState) {
	url := strings.TrimSpace(p.questionNotifyURL)
	if url == "" || run == nil {
		return
	}
	raw, err := json.Marshal(questionNotifyBody(run))
	if err != nil {
		slog.Warn("chat-api: question notify marshal", "error", err)
		return
	}
	secret := p.questionNotifySecret
	headers := p.questionNotifyHeaders
	timeout := p.questionNotifyTimeout
	if timeout <= 0 {
		timeout = defaultQuestionNotifyTimeout
	}
	go func() {
		client := &http.Client{Timeout: timeout}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		if err != nil {
			slog.Warn("chat-api: question notify request", "error", err)
			return
		}
		applyQuestionNotifyHeaders(req, headers, secret)
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("chat-api: question notify failed", "url", url, "error", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Warn("chat-api: question notify non-2xx", "url", url, "status", resp.StatusCode)
			return
		}
		slog.Info("chat-api: question notify sent",
			"url", url,
			"run_id", run.id,
			"conversation_id", run.conversationID,
			"message_id", run.messageID,
			"status", resp.StatusCode,
		)
	}()
}

func applyQuestionNotifyHeaders(req *http.Request, headers map[string]string, secret string) {
	if req == nil {
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" && req.Header.Get("X-Chat-API-Notify-Secret") == "" {
		req.Header.Set("X-Chat-API-Notify-Secret", secret)
	}
	if req.Header.Get("X-Chat-API-Event") == "" {
		req.Header.Set("X-Chat-API-Event", "question_request")
	}
}

package chatapi

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errUserCanceled = errors.New("canceled by user")

type pendingResult struct {
	answer       string
	err          error
	queued       bool
	queueFull    bool
	errMsg       string
	queueDepth   int
	userCanceled bool
}

// runState tracks one chat-messages SSE run and its in-flight agent turn.
type runState struct {
	id             string
	user           string
	sessionKey     string
	conversationID string
	messageID      string
	created        time.Time

	mu         sync.Mutex
	latestText string
	sentText   string
	finalized  bool
	sse        *sseWriter
	detached   bool

	notify chan struct{}
	done   chan pendingResult
	once   sync.Once
}

type pendingStore struct {
	mu   sync.Mutex
	runs map[string]*runState
	max  int
	ttl  time.Duration
}

func newPendingStore(max int, ttl time.Duration) *pendingStore {
	if max <= 0 {
		max = defaultMaxRuns
	}
	if ttl <= 0 {
		ttl = defaultRunTTL
	}
	return &pendingStore{
		runs: make(map[string]*runState),
		max:  max,
		ttl:  ttl,
	}
}

func (s *pendingStore) create(run *runState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now())
	if len(s.runs) >= s.max {
		return false
	}
	s.runs[run.id] = run
	return true
}

func (s *pendingStore) get(id string) *runState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func (s *pendingStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
}

func (s *pendingStore) cleanupLocked(now time.Time) {
	for id, run := range s.runs {
		if now.Sub(run.created) > s.ttl {
			run.complete(pendingResult{err: context.DeadlineExceeded})
			delete(s.runs, id)
		}
	}
}

func newRunState(id, user, sessionKey, conversationID, messageID string, sse *sseWriter) *runState {
	return &runState{
		id:             id,
		user:           user,
		sessionKey:     sessionKey,
		conversationID: conversationID,
		messageID:      messageID,
		created:        time.Now(),
		sse:            sse,
		notify:         make(chan struct{}, 1),
		done:           make(chan pendingResult, 1),
	}
}

func (r *runState) setLatestText(text string) {
	r.mu.Lock()
	r.latestText = text
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *runState) detach() {
	r.mu.Lock()
	r.detached = true
	r.sse = nil
	r.mu.Unlock()
}

func (r *runState) flushDelta() error {
	r.mu.Lock()
	sse := r.sse
	messageID := r.messageID
	if sse == nil || r.detached {
		r.mu.Unlock()
		return nil
	}
	curr := r.latestText
	prev := r.sentText
	r.mu.Unlock()

	delta := textDelta(prev, curr)
	if delta == "" {
		return nil
	}
	if err := sse.Event("text_delta", map[string]string{
		"message_id": messageID,
		"text":       delta,
	}); err != nil {
		r.detach()
		return err
	}
	r.mu.Lock()
	r.sentText = curr
	r.mu.Unlock()
	return nil
}

func (r *runState) complete(result pendingResult) bool {
	var ok bool
	r.once.Do(func() {
		r.done <- result
		ok = true
	})
	return ok
}

func (r *runState) replyContext() replyContext {
	return replyContext{
		runID:          r.id,
		conversationID: r.conversationID,
		messageID:      r.messageID,
	}
}

func (r *runState) latestAnswer(fallback string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fallback != "" {
		return fallback
	}
	return r.latestText
}

func (s *pendingStore) setLatestText(id, text string) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	run.setLatestText(text)
	return true
}

func (s *pendingStore) signalQueued(id string, depth int) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	if !run.complete(pendingResult{queued: true, queueDepth: depth}) {
		return false
	}
	s.delete(id)
	return true
}

func (s *pendingStore) signalQueueFull(id, msg string) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	if !run.complete(pendingResult{queueFull: true, errMsg: msg}) {
		return false
	}
	s.delete(id)
	return true
}

func (s *pendingStore) finish(id string, result pendingResult) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	run.mu.Lock()
	run.finalized = true
	if result.answer == "" {
		result.answer = run.latestText
	}
	run.mu.Unlock()
	if !run.complete(result) {
		return false
	}
	s.delete(id)
	return true
}

func (s *pendingStore) cancelUser(id string) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	if !run.complete(pendingResult{userCanceled: true, err: errUserCanceled}) {
		return false
	}
	s.delete(id)
	return true
}

func (s *pendingStore) cancelTimeout(id string) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	if !run.complete(pendingResult{err: context.DeadlineExceeded}) {
		return false
	}
	s.delete(id)
	return true
}

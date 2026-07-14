package chatapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	errUserCanceled         = errors.New("canceled by user")
	errInteractionTimedOut  = errors.New("interaction timed out")
	errInteractionResponded = errors.New("interaction already responded")
	errInteractionExpired   = errors.New("interaction expired")
)

type pendingResult struct {
	answer                 string
	err                    error
	queued                 bool
	queueFull              bool
	errMsg                 string
	queueDepth             int
	userCanceled           bool
	interactionTimedOut    bool
	interactionTimeoutKind string
}

type interactionKind string

const (
	interactionPermission interactionKind = "permission"
	interactionQuestion   interactionKind = "question"
)

type interactionAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type interactionState struct {
	ID        string
	Kind      interactionKind
	Prompt    string
	Actions   []interactionAction
	ExpiresAt time.Time
	Responded bool
	Expired   bool
}

type pendingSSEEvent struct {
	name    string
	payload any
}

// runState tracks one chat-messages SSE run and its in-flight agent turn.
type runState struct {
	id              string
	user            string
	channelKey      string
	sessionKey      string
	conversationID  string
	messageID       string
	created         time.Time
	requestDeadline time.Time

	mu                   sync.Mutex
	latestThinking       string
	sentThinking         string
	answerText           string
	sentAnswer           string
	finalized            bool
	streamingCardCreated bool
	sse                  *sseWriter
	detached             bool
	pendingEvents        []pendingSSEEvent
	interaction          *interactionState
	interactionTimer     *time.Timer

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
	if run := s.runs[id]; run != nil {
		run.stopInteractionTimer()
	}
	delete(s.runs, id)
}

func (s *pendingStore) cleanupLocked(now time.Time) {
	for id, run := range s.runs {
		if now.Sub(run.created) > s.ttl {
			run.stopInteractionTimer()
			run.complete(pendingResult{err: context.DeadlineExceeded})
			delete(s.runs, id)
		}
	}
}

func newRunState(id, user, channelKey, sessionKey, conversationID, messageID string, sse *sseWriter, requestDeadline time.Time) *runState {
	return &runState{
		id:              id,
		user:            user,
		channelKey:      channelKey,
		sessionKey:      sessionKey,
		conversationID:  conversationID,
		messageID:       messageID,
		created:         time.Now(),
		requestDeadline: requestDeadline,
		sse:             sse,
		notify:          make(chan struct{}, 1),
		done:            make(chan pendingResult, 1),
	}
}

func (r *runState) setStreamContent(thinking, answer string) {
	r.mu.Lock()
	r.latestThinking = thinking
	r.answerText = answer
	r.mu.Unlock()
	r.signal()
}

func (r *runState) signal() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *runState) enqueueEvent(name string, payload any) {
	r.mu.Lock()
	r.pendingEvents = append(r.pendingEvents, pendingSSEEvent{name: name, payload: payload})
	r.mu.Unlock()
	r.signal()
}

func (r *runState) detach() {
	r.mu.Lock()
	r.detached = true
	r.sse = nil
	r.mu.Unlock()
}

func (r *runState) flushDelta() error {
	if err := r.flushThinkingDelta(); err != nil {
		return err
	}
	if err := r.flushAnswerDelta(); err != nil {
		return err
	}
	return r.flushEvents()
}

func (r *runState) flushEvents() error {
	r.mu.Lock()
	events := r.pendingEvents
	r.pendingEvents = nil
	sse := r.sse
	detached := r.detached
	r.mu.Unlock()
	if sse == nil || detached {
		return nil
	}
	for _, ev := range events {
		if err := sse.Event(ev.name, ev.payload); err != nil {
			r.detach()
			return err
		}
	}
	return nil
}

func (r *runState) flushThinkingDelta() error {
	r.mu.Lock()
	sse := r.sse
	messageID := r.messageID
	if sse == nil || r.detached {
		r.mu.Unlock()
		return nil
	}
	curr := r.latestThinking
	prev := r.sentThinking
	r.mu.Unlock()

	delta := textDelta(prev, curr)
	if delta == "" {
		return nil
	}
	if err := sse.Event("thinking_delta", map[string]string{
		"message_id": messageID,
		"text":       delta,
	}); err != nil {
		r.detach()
		return err
	}
	r.mu.Lock()
	r.sentThinking = curr
	r.mu.Unlock()
	return nil
}

func (r *runState) flushAnswerDelta() error {
	r.mu.Lock()
	sse := r.sse
	messageID := r.messageID
	if sse == nil || r.detached {
		r.mu.Unlock()
		return nil
	}
	curr := r.answerText
	prev := r.sentAnswer
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
	r.sentAnswer = curr
	r.mu.Unlock()
	return nil
}

func (r *runState) complete(result pendingResult) bool {
	var ok bool
	r.once.Do(func() {
		r.stopInteractionTimer()
		r.done <- result
		ok = true
	})
	return ok
}

func (r *runState) replyContext() *replyContext {
	return &replyContext{
		runID:          r.id,
		conversationID: r.conversationID,
		messageID:      r.messageID,
	}
}

func (r *runState) interactionReplyContext(interactionID string) *replyContext {
	rc := r.replyContext()
	rc.interactionAck = true
	rc.interactionID = interactionID
	return rc
}

func (r *runState) markStreamingCardCreated() {
	r.mu.Lock()
	r.streamingCardCreated = true
	r.mu.Unlock()
}

func (r *runState) shouldFinishPlainReply() (answer string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized || r.streamingCardCreated {
		return "", false
	}
	if r.interaction != nil && !r.interaction.Responded && !r.interaction.Expired {
		return "", false
	}
	if strings.TrimSpace(r.answerText) == "" {
		return "", false
	}
	return r.answerText, true
}

func (r *runState) finalAnswer(fallback string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fallback != "" {
		return fallback
	}
	return r.answerText
}

func (r *runState) stopInteractionTimer() {
	r.mu.Lock()
	timer := r.interactionTimer
	r.interactionTimer = nil
	r.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (r *runState) getInteraction(id string) *interactionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.interaction == nil || r.interaction.ID != id {
		return nil
	}
	// Return a shallow copy for safe reads outside lock.
	cp := *r.interaction
	return &cp
}

func (r *runState) replaceInteraction(ix *interactionState, timer *time.Timer) *interactionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	var superseded *interactionState
	if r.interaction != nil && !r.interaction.Responded && !r.interaction.Expired {
		cp := *r.interaction
		superseded = &cp
	}
	if r.interactionTimer != nil {
		r.interactionTimer.Stop()
	}
	r.interaction = ix
	r.interactionTimer = timer
	return superseded
}

func (r *runState) markInteractionResponded(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.interaction == nil || r.interaction.ID != id {
		return errors.New("not found")
	}
	if r.interaction.Expired {
		return errInteractionExpired
	}
	if r.interaction.Responded {
		return errInteractionResponded
	}
	r.interaction.Responded = true
	if r.interactionTimer != nil {
		r.interactionTimer.Stop()
		r.interactionTimer = nil
	}
	return nil
}

func (r *runState) markInteractionExpired(id string) (*interactionState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.interaction == nil || r.interaction.ID != id {
		return nil, false
	}
	if r.interaction.Responded || r.interaction.Expired {
		return nil, false
	}
	r.interaction.Expired = true
	if r.interactionTimer != nil {
		r.interactionTimer.Stop()
		r.interactionTimer = nil
	}
	cp := *r.interaction
	return &cp, true
}

func (s *pendingStore) setStreamContent(id, content string) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	thinking, answer := parseStreamingCardContent(content)
	run.setStreamContent(thinking, answer)
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
		result.answer = run.answerText
	}
	run.mu.Unlock()
	if !run.complete(result) {
		return false
	}
	s.delete(id)
	return true
}

func (p *Platform) finishPlainReplyIfNeeded(runID string) {
	run := p.pending.get(runID)
	if run == nil {
		return
	}
	answer, ok := run.shouldFinishPlainReply()
	if !ok {
		return
	}
	p.pending.finish(runID, pendingResult{answer: answer})
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

func (s *pendingStore) cancelInteractionTimeout(id, kind string) bool {
	run := s.get(id)
	if run == nil {
		return false
	}
	if !run.complete(pendingResult{
		err:                    errInteractionTimedOut,
		interactionTimedOut:    true,
		interactionTimeoutKind: kind,
	}) {
		return false
	}
	s.delete(id)
	return true
}

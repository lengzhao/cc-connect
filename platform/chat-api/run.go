package chatapi

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	errUserCanceled         = errors.New("canceled by user")
	errInteractionTimedOut  = errors.New("interaction timed out")
	errInteractionResponded = errors.New("interaction already responded")
	errInteractionExpired   = errors.New("interaction expired")
	errRunAlreadyAttached   = errors.New("run already attached")
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
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value,omitempty"`
	Tag         string `json:"-"`
	TagVariant  string `json:"-"`
}

type interactionState struct {
	ID               string
	Kind             interactionKind
	Prompt           string
	Title            string
	Description      string
	AllowCustomInput bool
	Event            string
	Actions          []interactionAction
	MultiSelect      bool
	ExpiresAt        time.Time
	Responded        bool
	Expired          bool
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
	platform        *Platform

	mu                   sync.Mutex
	flushMu              sync.Mutex // serializes attach/detach/flushDelta
	latestThinking       string
	sentThinking         string
	answerText           string
	sentAnswer           string
	toolCalls            []streamToolCall
	sentToolCallIDs      map[string]bool
	pendingToolResults   []streamToolResult
	toolResultMatchIndex int
	finalized            bool
	streamingCardCreated bool
	structuredPrimary    bool // true after first TurnStreamEvent; skip markdown re-parse
	sink                 runEventSink
	detached             bool
	attaching            bool
	pendingEvents        []pendingSSEEvent
	interaction          *interactionState
	interactionTimer     *time.Timer
	lastRecoverableEvent *recoverableEvent

	notify chan struct{}
	done   chan pendingResult
	once   sync.Once
}

type pendingStore struct {
	mu   sync.Mutex
	runs map[string]*runState
	max  int
}

func newPendingStore(max int) *pendingStore {
	if max <= 0 {
		max = defaultMaxRuns
	}
	return &pendingStore{
		runs: make(map[string]*runState),
		max:  max,
	}
}

func (s *pendingStore) create(run *runState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func newRunState(id, user, channelKey, sessionKey, conversationID, messageID string, p *Platform, sse *sseWriter, requestDeadline time.Time) *runState {
	run := &runState{
		id:              id,
		user:            user,
		channelKey:      channelKey,
		sessionKey:      sessionKey,
		conversationID:  conversationID,
		messageID:       messageID,
		created:         time.Now(),
		requestDeadline: requestDeadline,
		platform:        p,
		sink:            &sseEventSink{w: sse},
		sentToolCallIDs: make(map[string]bool),
		notify:          make(chan struct{}, 1),
		done:            make(chan pendingResult, 1),
	}
	return run
}

func (r *runState) setStreamContent(thinking, answer string) {
	r.mu.Lock()
	r.latestThinking = thinking
	r.answerText = answer
	r.mu.Unlock()
	r.signal()
}

// contentUnchanged reports whether the run's current streamed thinking/answer
// already equal the given values (used by Finalize to skip a redundant re-set).
func (r *runState) contentUnchanged(thinking, answer string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latestThinking == thinking && r.answerText == answer
}

func (r *runState) usesStructuredStream() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.structuredPrimary
}

func (r *runState) setThinking(text string) {
	r.mu.Lock()
	r.structuredPrimary = true
	r.latestThinking = text
	r.mu.Unlock()
	r.signal()
}

func (r *runState) appendAnswer(suffix string) {
	if suffix == "" {
		return
	}
	r.mu.Lock()
	r.structuredPrimary = true
	r.answerText += suffix
	r.mu.Unlock()
	r.signal()
}

func (r *runState) replaceAnswer(full string) {
	r.mu.Lock()
	r.structuredPrimary = true
	r.answerText = full
	r.mu.Unlock()
	r.signal()
}

func (r *runState) upsertStructuredTool(id, name, input string) {
	r.mu.Lock()
	r.structuredPrimary = true
	r.mergeToolCallsLocked([]streamToolCall{{ID: id, Name: name, Input: input}})
	r.mu.Unlock()
	r.signal()
}

func (r *runState) enqueueStructuredToolResult(res streamToolResult) {
	r.mu.Lock()
	r.structuredPrimary = true
	r.pendingToolResults = append(r.pendingToolResults, res)
	r.mu.Unlock()
	r.signal()
}

func (r *runState) signal() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
	r.mu.Lock()
	detached := r.detached
	r.mu.Unlock()
	if detached {
		_ = r.flushDelta()
	}
}

func (r *runState) applyCardContent(content string) {
	thinking, answer := parseStreamingCardContent(content)
	tools := extractStreamingToolCalls(content)
	r.mu.Lock()
	r.latestThinking = thinking
	r.answerText = answer
	r.mergeToolCallsLocked(tools)
	r.mu.Unlock()
	r.signal()
}

func (r *runState) mergeToolCallsLocked(tools []streamToolCall) {
	known := make(map[string]bool, len(r.toolCalls))
	for _, tc := range r.toolCalls {
		known[tc.ID] = true
	}
	for _, tc := range tools {
		if known[tc.ID] {
			continue
		}
		r.toolCalls = append(r.toolCalls, tc)
		known[tc.ID] = true
	}
}

func (r *runState) enqueueEvent(name string, payload any) {
	r.mu.Lock()
	r.pendingEvents = append(r.pendingEvents, pendingSSEEvent{name: name, payload: payload})
	r.mu.Unlock()
	r.signal()
}

func (r *runState) detach() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.detachUnderFlush()
}

// detachUnderFlush switches to the virtual sink. Caller must hold flushMu.
func (r *runState) detachUnderFlush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.detached {
		return
	}
	r.detached = true
	r.sink = &detachedEventSink{run: r, p: r.platform}
}

func (r *runState) attach(sse *sseWriter) error {
	if err := r.beginAttach(); err != nil {
		return err
	}
	r.finishAttach(sse)
	return nil
}

func (r *runState) beginAttach() error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attaching || (r.sink != nil && r.sink.Active()) {
		return errRunAlreadyAttached
	}
	r.attaching = true
	return nil
}

func (r *runState) finishAttach(sse *sseWriter) {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detached = false
	r.attaching = false
	r.sink = &sseEventSink{w: sse}
}

func (r *runState) cancelAttach() {
	r.mu.Lock()
	r.attaching = false
	r.mu.Unlock()
}

func (r *runState) sinkActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attaching || (r.sink != nil && r.sink.Active())
}

func (r *runState) flushDelta() error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	if err := r.flushThinkingDelta(); err != nil {
		return err
	}
	if err := r.flushToolCallEvents(); err != nil {
		return err
	}
	if err := r.flushToolResultEvents(); err != nil {
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
	sink := r.sink
	r.mu.Unlock()
	if sink == nil {
		return nil
	}
	for _, ev := range events {
		if err := sink.Event(ev.name, ev.payload); err != nil {
			r.detachUnderFlush()
			return err
		}
	}
	return nil
}

func (r *runState) flushThinkingDelta() error {
	r.mu.Lock()
	sink := r.sink
	messageID := r.messageID
	curr := r.latestThinking
	prev := r.sentThinking
	active := sink != nil && sink.Active()
	r.mu.Unlock()
	if sink == nil {
		return nil
	}
	if !active {
		if curr == "" || curr == prev {
			return nil
		}
		// Prefer answer snapshots when both exist; thinking alone is recoverable.
		r.mu.Lock()
		hasAnswer := strings.TrimSpace(r.answerText) != ""
		r.mu.Unlock()
		if hasAnswer {
			return nil
		}
		payload := map[string]any{"message_id": messageID, "text": curr, "replace": true}
		if err := sink.Event("thinking_delta", payload); err != nil {
			return err
		}
		r.mu.Lock()
		r.sentThinking = curr
		r.mu.Unlock()
		return nil
	}

	payload, ok := deltaPayload(messageID, prev, curr)
	if !ok {
		return nil
	}
	if err := sink.Event("thinking_delta", payload); err != nil {
		r.detachUnderFlush()
		return err
	}
	r.mu.Lock()
	r.sentThinking = curr
	r.mu.Unlock()
	return nil
}

func (r *runState) flushToolCallEvents() error {
	r.mu.Lock()
	sink := r.sink
	messageID := r.messageID
	if sink == nil || !sink.Active() {
		r.mu.Unlock()
		return nil
	}
	var pending []streamToolCall
	for _, tc := range r.toolCalls {
		if r.sentToolCallIDs[tc.ID] {
			continue
		}
		pending = append(pending, tc)
	}
	r.mu.Unlock()

	for _, tc := range pending {
		payload := map[string]any{
			"message_id":   messageID,
			"tool_call_id": tc.ID,
			"name":         tc.Name,
		}
		if tc.Input != "" {
			payload["input"] = tc.Input
		}
		if err := sink.Event("tool_call", payload); err != nil {
			r.detachUnderFlush()
			return err
		}
		r.mu.Lock()
		r.sentToolCallIDs[tc.ID] = true
		r.mu.Unlock()
	}
	return nil
}

func (r *runState) flushToolResultEvents() error {
	r.mu.Lock()
	sink := r.sink
	messageID := r.messageID
	if sink == nil || !sink.Active() {
		r.mu.Unlock()
		return nil
	}
	pending := append([]streamToolResult(nil), r.pendingToolResults...)
	r.pendingToolResults = nil
	r.mu.Unlock()

	for _, res := range pending {
		r.mu.Lock()
		toolCallID := r.nextToolCallIDLocked(res.Name)
		r.toolResultMatchIndex++
		r.mu.Unlock()

		payload := map[string]any{
			"message_id":   messageID,
			"tool_call_id": toolCallID,
		}
		if res.Name != "" {
			payload["name"] = res.Name
		}
		if res.Status != "" {
			payload["status"] = res.Status
		}
		if res.ExitCode != nil {
			payload["exit_code"] = *res.ExitCode
		}
		if res.Success != nil {
			payload["success"] = *res.Success
		}
		if res.Output != "" {
			payload["output"] = res.Output
		}
		if err := sink.Event("tool_result", payload); err != nil {
			r.detachUnderFlush()
			return err
		}
	}
	return nil
}

func (r *runState) nextToolCallIDLocked(_ string) string {
	idx := r.toolResultMatchIndex
	if idx < len(r.toolCalls) {
		return r.toolCalls[idx].ID
	}
	return strconv.Itoa(idx + 1)
}

func (r *runState) flushAnswerDelta() error {
	r.mu.Lock()
	sink := r.sink
	messageID := r.messageID
	curr := r.answerText
	prev := r.sentAnswer
	active := sink != nil && sink.Active()
	r.mu.Unlock()
	if sink == nil {
		return nil
	}
	if !active {
		if curr == "" || curr == prev {
			return nil
		}
		payload := map[string]any{"message_id": messageID, "text": curr, "replace": true}
		if err := sink.Event("text_delta", payload); err != nil {
			return err
		}
		r.mu.Lock()
		r.sentAnswer = curr
		r.mu.Unlock()
		return nil
	}

	payload, ok := deltaPayload(messageID, prev, curr)
	if !ok {
		return nil
	}
	if err := sink.Event("text_delta", payload); err != nil {
		r.detachUnderFlush()
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
	run.applyCardContent(content)
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
	run.lastRecoverableEvent = nil
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
	run.mu.Lock()
	run.finalized = true
	run.lastRecoverableEvent = nil
	run.mu.Unlock()
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
	run.mu.Lock()
	run.finalized = true
	run.lastRecoverableEvent = nil
	run.mu.Unlock()
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

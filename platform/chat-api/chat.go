package chatapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

const (
	maxRequestBody            = 10 << 20 // 10 MiB
	defaultMaxRuns            = 1000
	defaultInteractionTimeout = 10 * time.Minute
	defaultSSEPingInterval    = 5 * time.Second
	busyPolicyQueue           = "queue"
	busyPolicyReject          = "reject"
)

type chatInput struct {
	Type           string `json:"type"`
	TransferMethod string `json:"transfer_method"`
	Data           string `json:"data"`
	UploadFileID   string `json:"upload_file_id"`
	MimeType       string `json:"mime_type"`
	Filename       string `json:"filename"`
}

type chatRequest struct {
	ConversationID   string         `json:"conversation_id"`
	Query            string         `json:"query"`
	RunID            string         `json:"run_id"`
	Inputs           []chatInput    `json:"inputs"`
	AutoGenerateName *bool          `json:"auto_generate_name"`
	Metadata         map[string]any `json:"metadata"`
}

type replyContext struct {
	runID          string
	conversationID string
	messageID      string
	metadata       map[string]any
	headers        map[string]string // whitelisted inbound HTTP headers (hooks only, not agent prompt)
	interactionAck bool              // Reply is an interaction acknowledgment; do not end the turn
	interactionID  string
}

func (p *Platform) handleChatMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	user, ok := p.resolveUser(w, r, true)
	if !ok {
		return
	}
	userName := displayUserName(user, p.resolveUserName(r))
	channelKey, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	accept := r.Header.Get("Accept")
	if accept != "" && !strings.Contains(accept, "text/event-stream") && !strings.Contains(accept, "*/*") {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	var body chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&body); err != nil {
		if err == io.EOF {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if runID := strings.TrimSpace(body.RunID); runID != "" {
		p.handleChatResume(w, r, user, runID, strings.TrimSpace(body.ConversationID))
		return
	}

	sessions := p.sessionsOrReload()
	if sessions == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	query := strings.TrimSpace(body.Query)
	if query == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	sessionKey := sessionKeyForChannel(channelKey)
	var session *core.Session
	implicitCreate := strings.TrimSpace(body.ConversationID) == ""
	if implicitCreate {
		id, err := newConversationID()
		if err != nil {
			slog.Error("chat-api: conversation id", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		session, err = sessions.NewSessionWithID(sessionKey, id, "default")
		if err != nil {
			slog.Error("chat-api: create session", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		session.SetCreatedBy(user)
	} else {
		session = p.findConversationInChannel(sessions, channelKey, body.ConversationID)
		if session == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		_, _ = sessions.SwitchSession(sessionKey, body.ConversationID)
	}
	engineSessionKey := engineSessionKey(channelKey, session.ID)
	sessions.BindActiveSession(engineSessionKey, session.ID)

	if p.busyPolicy == busyPolicyReject && session.Busy() {
		writeErr(w, http.StatusConflict, "conversation busy")
		return
	}

	images, files, audio, uploadPaths, err := p.inputsToCore(channelKey, body.Inputs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(uploadPaths) > 0 {
		query = core.AppendFileRefs(query, uploadPaths)
	}

	turnIndex := countCompletedTurns(session.GetHistory(0))
	msgID := messageID(session.ID, turnIndex)
	runID := newRunID()

	sse, err := newSSEWriter(w)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	requestDeadline := time.Now().Add(p.requestTimeout)
	run := newRunState(runID, user, channelKey, engineSessionKey, session.ID, msgID, p, sse, requestDeadline)
	if !p.pending.create(run) {
		_ = sse.Error("too many concurrent requests")
		return
	}

	rc := &replyContext{
		runID:          runID,
		conversationID: session.ID,
		messageID:      msgID,
		metadata:       body.Metadata,
		headers:        collectForwardedHeaders(p.forwardHeaders, r),
	}

	if err := sse.Event("message", map[string]any{
		"conversation_id": session.ID,
		"message_id":      msgID,
		"run_id":          runID,
	}); err != nil {
		logSSELifecycle("disconnect", run, "reason", "write_error", "error", err.Error())
		run.detach()
		return
	}
	logSSELifecycle("start", run)

	autoName := body.AutoGenerateName == nil || *body.AutoGenerateName
	chatName, _ := p.ResolveChannelName(channelKey)
	if err := p.ensureChannelWorkspace(channelKey); err != nil {
		slog.Error("chat-api: ensure channel workspace", "channel", channelKey, "error", err)
		_ = sse.Error("internal error")
		p.pending.finish(runID, pendingResult{err: err})
		return
	}
	msg := core.Message{
		SessionKey:   engineSessionKey,
		Platform:     p.Name(),
		MessageID:    runID,
		ChannelID:    channelKey,
		ChannelKey:   channelKey,
		ChatName:     chatName,
		UserID:       user,
		UserName:     userName,
		Content:      query,
		Images:       images,
		Files:        files,
		Audio:        audio,
		ReplyCtx:     rc,
		AgentContext: p.agentContextHeaders.collectAgentContext(r),
	}

	if implicitCreate && autoName {
		p.startAutoNameGeneration(newNameRunID(), session, sessions, query)
	}

	handler := p.getHandler()
	if handler == nil {
		_ = sse.Error("internal error")
		p.pending.finish(runID, pendingResult{err: errors.New("handler not ready")})
		return
	}

	go func() {
		handler(p, &msg)
		p.finishPlainReplyIfNeeded(runID)
	}()

	p.serveRunSSE(r.Context(), run, sse, engineSessionKey, user, channelKey, rc)
}

func (p *Platform) handleChatResume(w http.ResponseWriter, r *http.Request, user, runID, conversationID string) {
	run := p.pending.get(runID)
	if run == nil || run.user != user {
		reason := "run_not_found"
		if run != nil {
			reason = "user_mismatch"
		}
		logSSELifecyclePartial("resume_miss", runID, user, conversationID, "reason", reason)
		p.writeResumeMessageEnd(w, conversationID)
		return
	}

	if err := run.beginAttach(); err != nil {
		logSSELifecycle("resume_rejected", run, "reason", "already_attached")
		writeErr(w, http.StatusConflict, "run already attached")
		return
	}

	sse, err := newSSEWriter(w)
	if err != nil {
		run.cancelAttach()
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	run.finishAttach(sse)
	replayEvent := ""
	if ev := run.peekLastRecoverable(); ev != nil {
		replayEvent = ev.name
		if err := sse.Event(ev.name, ev.payload); err != nil {
			logSSELifecycle("disconnect", run, "reason", "write_error", "error", err.Error())
			run.detach()
			return
		}
		run.clearLastRecoverable()
	}
	if replayEvent != "" {
		logSSELifecycle("resume", run, "replay_event", replayEvent)
	} else {
		logSSELifecycle("resume", run)
	}

	rc := run.replyContext()
	p.serveRunSSE(r.Context(), run, sse, run.sessionKey, user, run.channelKey, rc)
}

func (p *Platform) writeResumeMessageEnd(w http.ResponseWriter, conversationID string) {
	sse, err := newSSEWriter(w)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	payload := map[string]any{}
	if conversationID != "" {
		payload["conversation_id"] = conversationID
	}
	_ = sse.Event("message_end", payload)
}

func (p *Platform) serveRunSSE(reqCtx context.Context, run *runState, sse *sseWriter, engineSessionKey, user, channelKey string, rc *replyContext) {
	var deadlineC <-chan time.Time
	var deadline *time.Timer
	remaining := p.requestTimeout
	if !run.requestDeadline.IsZero() {
		remaining = time.Until(run.requestDeadline)
	}
	if remaining > 0 {
		deadline = time.NewTimer(remaining)
		defer deadline.Stop()
		deadlineC = deadline.C
	}

	var pingTicker *time.Ticker
	var pingC <-chan time.Time
	if p.ssePingInterval > 0 {
		pingTicker = time.NewTicker(p.ssePingInterval)
		defer pingTicker.Stop()
		pingC = pingTicker.C
	}

	for {
		select {
		case <-run.notify:
			if err := run.flushDelta(); err != nil {
				logSSELifecycle("disconnect", run, "reason", "write_error", "error", err.Error())
				return
			}
		case result := <-run.done:
			p.emitTerminalSSE(run, result)
			p.pending.delete(run.id)
			return
		case <-pingC:
			run.enqueueEvent("ping", map[string]any{
				"run_id": run.id,
				"ts":     time.Now().Unix(),
			})
		case <-deadlineC:
			p.dispatchStop(engineSessionKey, user, channelKey, rc)
			p.pending.cancelTimeout(run.id)
			_ = sse.Error("request timed out")
			return
		case <-reqCtx.Done():
			logSSELifecycle("disconnect", run, "reason", "client_gone")
			run.detach()
			return
		}
	}
}

func (p *Platform) emitTerminalSSE(run *runState, result pendingResult) {
	run.mu.Lock()
	sink := run.sink
	msgID := run.messageID
	conversationID := run.conversationID
	run.mu.Unlock()
	if sink == nil || !sink.Active() {
		return
	}
	_ = run.flushDelta()

	switch {
	case result.queued:
		_ = sink.Event("message_queued", map[string]any{
			"message_id":  msgID,
			"queue_depth": result.queueDepth,
		})
	case result.queueFull:
		_ = sink.Event("error", map[string]string{"error": result.errMsg})
	case result.userCanceled:
		_ = sink.Event("error", map[string]string{"error": errUserCanceled.Error()})
	case result.interactionTimedOut || errors.Is(result.err, errInteractionTimedOut):
		payload := map[string]any{
			"error": errInteractionTimedOut.Error(),
		}
		if result.interactionTimeoutKind != "" {
			payload["kind"] = result.interactionTimeoutKind
		}
		_ = sink.Event("error", payload)
	case result.err != nil:
		_ = sink.Event("error", map[string]string{"error": result.err.Error()})
	default:
		payload := map[string]string{
			"message_id":      msgID,
			"conversation_id": conversationID,
		}
		if p.includeAnswerInMessageEnd {
			if ans := run.finalAnswer(result.answer); ans != "" {
				payload["answer"] = ans
			}
		}
		_ = sink.Event("message_end", payload)
	}
}

// deltaPayload builds an SSE delta payload for a text/thinking stream. When curr
// extends prev it emits the appended suffix (client appends). When curr is not a
// continuation of prev — a progress line replaced by the final answer, or any
// non-prefix revision — it emits the full text with replace=true so an appending
// client discards what it had and replaces, instead of concatenating a duplicate.
// Returns ok=false when there is nothing to send.
func deltaPayload(messageID, prev, curr string) (map[string]any, bool) {
	if strings.HasPrefix(curr, prev) {
		suffix := curr[len(prev):]
		if suffix == "" {
			return nil, false
		}
		return map[string]any{"message_id": messageID, "text": suffix}, true
	}
	return replaceDeltaPayload(messageID, curr), true
}

const logContentLimit = 40

func truncateForLog(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	return string([]rune(s)[:maxLen])
}

// replaceDeltaPayload builds a full-text delta with replace=true and warns.
func replaceDeltaPayload(messageID, curr string) map[string]any {
	slog.Warn("chat-api: emitting SSE delta with replace=true",
		"message_id", messageID,
		"text", truncateForLog(curr, logContentLimit),
	)
	return map[string]any{"message_id": messageID, "text": curr, "replace": true}
}

func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "run_" + base64.RawURLEncoding.EncodeToString(b[:])
}

func (p *Platform) Reply(_ context.Context, replyTo any, content string) error {
	rc, ok := replyTo.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return fmt.Errorf("chat-api: unsupported reply context %T", replyTo)
	}
	if rc.interactionAck {
		run := p.pending.get(rc.runID)
		if run == nil {
			return fmt.Errorf("chat-api: run %q is not pending", rc.runID)
		}
		payload := map[string]any{
			"message_id": rc.messageID,
			"text":       content,
		}
		if rc.interactionID != "" {
			payload["interaction_id"] = rc.interactionID
		}
		run.enqueueEvent("interaction_ack", payload)
		return nil
	}
	if kind, depth, msg := classifySystemReply(content); kind != replyKindContent {
		switch kind {
		case replyKindQueued:
			if !p.pending.signalQueued(rc.runID, depth) {
				return fmt.Errorf("chat-api: run %q is not pending", rc.runID)
			}
		case replyKindQueueFull:
			if !p.pending.signalQueueFull(rc.runID, msg) {
				return fmt.Errorf("chat-api: run %q is not pending", rc.runID)
			}
		}
		return nil
	}
	if isToolResultFallbackMarkdown(content) {
		// Phase 3: tool_result is carried by StructuredStreamingCard events.
		// Ignore legacy 🧾 Reply markdown so it never becomes text_delta.
		return nil
	}
	if !p.pending.setStreamContent(rc.runID, content) {
		return fmt.Errorf("chat-api: run %q is not pending", rc.runID)
	}
	p.finishPlainReplyIfNeeded(rc.runID)
	return nil
}

func (p *Platform) Send(_ context.Context, replyCtx any, content string) error {
	return p.Reply(context.Background(), replyCtx, content)
}

func (p *Platform) CreateStreamingCard(_ context.Context, replyTo any) (core.StreamingCard, error) {
	rc, ok := replyTo.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return nil, errors.New("chat-api: invalid streaming card reply context")
	}
	if run := p.pending.get(rc.runID); run != nil {
		run.markStreamingCardCreated()
	}
	return &streamingCard{platform: p, rc: rc}, nil
}

func (p *Platform) OnProcessingEnd(_ context.Context, replyCtx any, _ core.ProcessingEndEvent) error {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return fmt.Errorf("chat-api: unsupported processing-end context %T", replyCtx)
	}
	run := p.pending.get(rc.runID)
	if run == nil {
		return nil
	}
	// Fallback for synchronous command paths; normal SSE turns complete via streamingCard.Finalize.
	p.pending.finish(rc.runID, pendingResult{answer: run.finalAnswer("")})
	return nil
}

// HookContext returns whitelisted inbound headers and body metadata for this
// turn. Values are delivered only to cc-connect hooks, not to the agent prompt.
func (p *Platform) HookContext(replyCtx any) core.HookContext {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil {
		return core.HookContext{}
	}
	out := core.HookContext{}
	if len(rc.headers) > 0 {
		out.Headers = make(map[string]string, len(rc.headers))
		for k, v := range rc.headers {
			out.Headers[k] = v
		}
	}
	if len(rc.metadata) > 0 {
		out.Context = make(map[string]any, len(rc.metadata))
		for k, v := range rc.metadata {
			out.Context[k] = v
		}
	}
	return out
}

type streamingCard struct {
	platform *Platform
	rc       *replyContext
	lastSent string
}

func (c *streamingCard) runID() string {
	if c.rc == nil {
		return ""
	}
	return c.rc.runID
}

// OnTurnStreamEvent implements core.StructuredStreamingCard. Primary path for
// Engine dual-write / Phase 2: typed events drive SSE without Markdown parsing.
func (c *streamingCard) OnTurnStreamEvent(_ context.Context, ev core.TurnStreamEvent) error {
	id := c.runID()
	run := c.platform.pending.get(id)
	if run == nil {
		return fmt.Errorf("chat-api: run %q is not pending", id)
	}
	switch ev.Kind {
	case core.TurnStreamThinkingReplace:
		run.setThinking(ev.Thinking)
	case core.TurnStreamAnswerAppend:
		run.appendAnswer(ev.Answer)
	case core.TurnStreamAnswerReplace:
		run.replaceAnswer(ev.Answer)
	case core.TurnStreamToolUpsert:
		run.upsertStructuredTool(strconv.Itoa(ev.Tool.Index), ev.Tool.Name, ev.Tool.Input)
	case core.TurnStreamToolResult:
		res := streamToolResult{Name: ev.Tool.Name}
		if ev.Tool.Result != nil {
			res.Output = ev.Tool.Result.Output
			res.Status = ev.Tool.Result.Status
			res.ExitCode = ev.Tool.Result.ExitCode
			res.Success = ev.Tool.Result.Success
			if res.Name == "" {
				res.Name = ev.Tool.Name
			}
		}
		run.enqueueStructuredToolResult(res)
	default:
		slog.Debug("chat-api: ignoring unknown turn stream event", "kind", int(ev.Kind), "run_id", id)
	}
	return nil
}

func (c *streamingCard) Update(_ context.Context, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	id := c.runID()
	// Phase 2: once structured events are active, ignore markdown dual-write.
	if run := c.platform.pending.get(id); run != nil && run.usesStructuredStream() {
		return nil
	}
	c.lastSent = content
	if !c.platform.pending.setStreamContent(id, content) {
		return fmt.Errorf("chat-api: run %q is not pending", id)
	}
	return nil
}

func (c *streamingCard) Finalize(_ context.Context, content string) error {
	id := c.runID()
	run := c.platform.pending.get(id)
	if run != nil && run.usesStructuredStream() {
		// Prefer answer already streamed via TurnStreamEvent; Engine Finalize may
		// still pass markdown which we deliberately ignore.
		answer := run.finalAnswer("")
		if !c.platform.pending.finish(id, pendingResult{answer: answer}) {
			return fmt.Errorf("chat-api: run %q is not pending", id)
		}
		return nil
	}

	raw := c.lastSent
	if strings.TrimSpace(content) != "" {
		raw = content
		c.lastSent = raw
	}
	thinking, answer := parseStreamingCardContent(raw)
	if strings.TrimSpace(raw) != "" {
		// Skip the redundant re-set when the final card matches what Update
		// already streamed — avoids emitting an extra terminal delta frame.
		if run == nil || !run.contentUnchanged(thinking, answer) {
			if !c.platform.pending.setStreamContent(id, raw) {
				return fmt.Errorf("chat-api: run %q is not pending", id)
			}
		}
	}
	// Never fall back to raw card markdown — that would reintroduce tool blocks.
	if !c.platform.pending.finish(id, pendingResult{answer: answer}) {
		return fmt.Errorf("chat-api: run %q is not pending", id)
	}
	return nil
}

func (c *streamingCard) Failed() bool {
	return false
}

var _ core.StreamingCard = (*streamingCard)(nil)
var _ core.StructuredStreamingCard = (*streamingCard)(nil)

type replyKind int

const (
	replyKindContent replyKind = iota
	replyKindQueued
	replyKindQueueFull
)

var (
	queuedReplyTexts  map[string]struct{}
	queueFullPrefixes []string
)

func initReplyClassifiers() {
	queuedReplyTexts = make(map[string]struct{})
	queueFullPrefixes = nil
	langs := []core.Language{
		core.LangEnglish,
		core.LangChinese,
		core.LangTraditionalChinese,
		core.LangJapanese,
		core.LangSpanish,
	}
	for _, lang := range langs {
		i18n := core.NewI18n(lang)
		queuedReplyTexts[i18n.T(core.MsgMessageQueued)] = struct{}{}
		tmpl := i18n.T(core.MsgQueueFull)
		if idx := strings.Index(tmpl, "%"); idx >= 0 {
			queueFullPrefixes = append(queueFullPrefixes, tmpl[:idx])
		}
	}
}

func classifySystemReply(content string) (replyKind, int, string) {
	if queuedReplyTexts == nil {
		initReplyClassifiers()
	}
	if _, ok := queuedReplyTexts[content]; ok {
		return replyKindQueued, 1, ""
	}
	for _, prefix := range queueFullPrefixes {
		if strings.HasPrefix(content, prefix) {
			depth := 1
			rest := strings.TrimPrefix(content, prefix)
			if n, err := fmt.Sscanf(rest, "%d", &depth); n == 1 && err == nil && depth > 0 {
				return replyKindQueueFull, depth, content
			}
			return replyKindQueueFull, 1, content
		}
	}
	return replyKindContent, 0, ""
}

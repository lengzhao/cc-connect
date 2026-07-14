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
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	maxRequestBody            = 10 << 20 // 10 MiB
	defaultMaxRuns            = 1000
	defaultRunTTL             = 2 * time.Hour
	defaultInteractionTimeout = 10 * time.Minute
	defaultSSEPingInterval    = 15 * time.Second
	busyPolicyQueue           = "queue"
	busyPolicyReject          = "reject"
)

type chatInput struct {
	Type           string `json:"type"`
	TransferMethod string `json:"transfer_method"`
	Data           string `json:"data"`
	MimeType       string `json:"mime_type"`
	Filename       string `json:"filename"`
}

type chatRequest struct {
	ConversationID   string         `json:"conversation_id"`
	Query            string         `json:"query"`
	Inputs           []chatInput    `json:"inputs"`
	AutoGenerateName *bool          `json:"auto_generate_name"`
	Metadata         map[string]any `json:"metadata"`
}

type replyContext struct {
	runID          string
	conversationID string
	messageID      string
	metadata       map[string]any
	interactionAck bool // Reply is an interaction acknowledgment; do not end the turn
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
	channelKey = p.channelKeyForMessage(channelKey)
	sessions := p.sessionsOrReload()
	if sessions == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
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
	query := strings.TrimSpace(body.Query)
	if query == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	sessionKey := sessionKeyForUser(user)
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
	} else {
		session = sessions.FindByID(body.ConversationID)
		if session == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if p.sessionOwnedByUser(sessions, user, body.ConversationID) {
			_, _ = sessions.SwitchSession(sessionKey, body.ConversationID)
		}
	}
	engineSessionKey := engineSessionKey(channelKey, session.ID)
	sessions.BindActiveSession(engineSessionKey, session.ID)

	if p.busyPolicy == busyPolicyReject && session.Busy() {
		writeErr(w, http.StatusConflict, "conversation busy")
		return
	}

	images, files, audio, err := inputsToCore(body.Inputs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
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
	run := newRunState(runID, user, channelKey, engineSessionKey, session.ID, msgID, sse, requestDeadline)
	if !p.pending.create(run) {
		_ = sse.Error("too many concurrent requests")
		return
	}

	rc := &replyContext{
		runID:          runID,
		conversationID: session.ID,
		messageID:      msgID,
		metadata:       body.Metadata,
	}

	if err := sse.Event("message", map[string]string{
		"conversation_id": session.ID,
		"message_id":      msgID,
		"run_id":          runID,
	}); err != nil {
		run.detach()
		return
	}

	autoName := body.AutoGenerateName == nil || *body.AutoGenerateName
	chatName, _ := p.ResolveChannelName(channelKey)
	if err := p.ensureChannelWorkspace(channelKey); err != nil {
		slog.Error("chat-api: ensure channel workspace", "channel", channelKey, "error", err)
		_ = sse.Error("internal error")
		p.pending.finish(runID, pendingResult{err: err})
		return
	}
	msg := core.Message{
		SessionKey: engineSessionKey,
		Platform:   p.Name(),
		MessageID:  runID,
		ChannelID:  channelKey,
		ChannelKey: channelKey,
		ChatName:   chatName,
		UserID:     user,
		UserName:   userName,
		Content:    query,
		Images:     images,
		Files:      files,
		Audio:      audio,
		ReplyCtx:   rc,
	}

	handler := p.getHandler()
	if handler == nil {
		_ = sse.Error("internal error")
		p.pending.finish(runID, pendingResult{err: errors.New("handler not ready")})
		return
	}

	reqCtx := r.Context()
	go func() {
		handler(p, &msg)
		p.finishPlainReplyIfNeeded(runID)
	}()

	if implicitCreate && autoName {
		defer func() {
			if session.GetName() == "default" {
				session.SetName(autoNameFromQuery(query))
				sessions.Save()
			}
		}()
	}

	deadline := time.NewTimer(p.requestTimeout)
	defer deadline.Stop()

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
				return
			}
		case result := <-run.done:
			p.emitTerminalSSE(run, result)
			return
		case <-pingC:
			run.enqueueEvent("ping", map[string]any{
				"run_id": runID,
				"ts":     time.Now().Unix(),
			})
		case <-deadline.C:
			p.dispatchStop(engineSessionKey, user, channelKey, rc)
			p.pending.cancelTimeout(runID)
			_ = sse.Error("request timed out")
			return
		case <-reqCtx.Done():
			run.detach()
			return
		}
	}
}

func (p *Platform) emitTerminalSSE(run *runState, result pendingResult) {
	run.mu.Lock()
	sse := run.sse
	msgID := run.messageID
	conversationID := run.conversationID
	run.mu.Unlock()
	if sse == nil {
		return
	}
	_ = run.flushDelta()

	switch {
	case result.queued:
		_ = sse.Event("message_queued", map[string]any{
			"message_id":  msgID,
			"queue_depth": result.queueDepth,
		})
	case result.queueFull:
		_ = sse.Error(result.errMsg)
	case result.userCanceled:
		_ = sse.Error(errUserCanceled.Error())
	case result.interactionTimedOut || errors.Is(result.err, errInteractionTimedOut):
		payload := map[string]any{
			"error": errInteractionTimedOut.Error(),
		}
		if result.interactionTimeoutKind != "" {
			payload["kind"] = result.interactionTimeoutKind
		}
		_ = sse.Event("error", payload)
	case result.err != nil:
		_ = sse.Error(result.err.Error())
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
		_ = sse.Event("message_end", payload)
	}
}

func textDelta(prev, curr string) string {
	if strings.HasPrefix(curr, prev) {
		return curr[len(prev):]
	}
	return curr
}

func inputsToCore(inputs []chatInput) ([]core.ImageAttachment, []core.FileAttachment, *core.AudioAttachment, error) {
	var images []core.ImageAttachment
	var files []core.FileAttachment
	var audio *core.AudioAttachment
	for _, in := range inputs {
		if !strings.EqualFold(in.TransferMethod, "base64") {
			return nil, nil, nil, fmt.Errorf("unsupported transfer_method")
		}
		data, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			return nil, nil, nil, err
		}
		switch strings.ToLower(in.Type) {
		case "image":
			images = append(images, core.ImageAttachment{
				MimeType: in.MimeType,
				Data:     data,
				FileName: in.Filename,
			})
		case "file":
			files = append(files, core.FileAttachment{
				MimeType: in.MimeType,
				Data:     data,
				FileName: in.Filename,
			})
		case "audio":
			if audio != nil {
				return nil, nil, nil, fmt.Errorf("only one audio input supported")
			}
			audio = &core.AudioAttachment{
				MimeType: in.MimeType,
				Data:     data,
			}
		default:
			return nil, nil, nil, fmt.Errorf("unsupported input type")
		}
	}
	return images, files, audio, nil
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

func (p *Platform) HookContext(replyCtx any) core.HookContext {
	rc, ok := replyCtx.(*replyContext)
	if !ok || rc == nil || len(rc.metadata) == 0 {
		return core.HookContext{}
	}
	out := core.HookContext{Context: make(map[string]any, len(rc.metadata))}
	for k, v := range rc.metadata {
		out.Context[k] = v
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

func (c *streamingCard) Update(_ context.Context, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	c.lastSent = content
	id := c.runID()
	if !c.platform.pending.setStreamContent(id, content) {
		return fmt.Errorf("chat-api: run %q is not pending", id)
	}
	return nil
}

func (c *streamingCard) Finalize(_ context.Context, content string) error {
	raw := c.lastSent
	if strings.TrimSpace(content) != "" {
		raw = content
		c.lastSent = raw
	}
	id := c.runID()
	if strings.TrimSpace(raw) != "" {
		if !c.platform.pending.setStreamContent(id, raw) {
			return fmt.Errorf("chat-api: run %q is not pending", id)
		}
	}
	_, answer := parseStreamingCardContent(raw)
	if answer == "" {
		answer = strings.TrimSpace(raw)
	}
	if !c.platform.pending.finish(id, pendingResult{answer: answer}) {
		return fmt.Errorf("chat-api: run %q is not pending", id)
	}
	return nil
}

func (c *streamingCard) Failed() bool {
	return false
}

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

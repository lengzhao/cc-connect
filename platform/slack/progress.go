package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
)

const (
	slackProgressKeepalive    = 25 * time.Second
	slackStatusKeepalive      = 90 * time.Second
	slackProgressAPITimeout   = 15 * time.Second
	progressStyleAssistantStatus = "assistant_status"
	progressStyleStream          = "stream"
)

type progressPlatform struct {
	*Platform
}

func (p *progressPlatform) ProgressStyle() string {
	if p.isNativeProgressEnabled() {
		return "card"
	}
	return "legacy"
}

func (p *progressPlatform) SupportsProgressCardPayload() bool {
	return p.isNativeProgressEnabled()
}

func (p *progressPlatform) ProgressUpdateInterval() time.Duration {
	if p.nativeProgressMode() == progressStyleStream {
		return 500 * time.Millisecond
	}
	return time.Second
}

func (p *Platform) isNativeProgressEnabled() bool {
	mode := p.nativeProgressMode()
	return mode == progressStyleAssistantStatus || mode == progressStyleStream
}

func (p *Platform) nativeProgressMode() string {
	if p == nil {
		return "legacy"
	}
	return strings.ToLower(strings.TrimSpace(p.progressStyle))
}

type nativeProgressHandle struct {
	replyCtx replyContext
	mode     string
	stream   *streamProgressHandle
	degraded bool
}

type streamProgressHandle struct {
	channel       string
	threadTS      string
	streamTS      string
	cancelKeep    context.CancelFunc
	lastSentCount int
	wasTruncated  bool
}

func (p *Platform) threadTarget(replyCtx any) (channel, threadTS string, ok bool) {
	rc, ok := replyCtx.(replyContext)
	if !ok || rc.channel == "" {
		return "", "", false
	}
	threadTS = strings.TrimSpace(rc.timestamp)
	if threadTS == "" {
		threadTS = strings.TrimSpace(rc.messageTS)
	}
	if threadTS == "" {
		return "", "", false
	}
	return rc.channel, threadTS, true
}

// SendPreviewStart implements core.PreviewStarter for native Slack progress.
func (p *Platform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	if !p.isNativeProgressEnabled() {
		return nil, fmt.Errorf("slack: native progress not enabled")
	}
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("slack: invalid reply context type %T", rctx)
	}
	payload, ok := core.ParseProgressCardPayload(content)
	if !ok || payload == nil {
		return nil, fmt.Errorf("slack: invalid progress payload")
	}
	handle := &nativeProgressHandle{
		replyCtx: rc,
		mode:     p.nativeProgressMode(),
	}
	if err := p.applyNativeProgressPayload(ctx, handle, payload); err != nil {
		return nil, err
	}
	return handle, nil
}

// UpdateMessage implements core.MessageUpdater for native Slack progress.
func (p *Platform) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	if !p.isNativeProgressEnabled() {
		return fmt.Errorf("slack: native progress not enabled")
	}
	handle, ok := previewHandle.(*nativeProgressHandle)
	if !ok || handle == nil {
		return fmt.Errorf("slack: invalid native progress handle %T", previewHandle)
	}
	payload, ok := core.ParseProgressCardPayload(content)
	if !ok || payload == nil {
		return fmt.Errorf("slack: invalid progress payload")
	}
	return p.applyNativeProgressPayload(ctx, handle, payload)
}

func (p *Platform) applyNativeProgressPayload(ctx context.Context, handle *nativeProgressHandle, payload *core.ProgressCardPayload) error {
	if handle == nil || payload == nil {
		return nil
	}
	items := payload.Items
	if len(items) == 0 {
		return nil
	}

	switch payload.State {
	case core.ProgressCardStateCompleted, core.ProgressCardStateFailed:
		return p.finishNativeProgress(ctx, handle, payload.State)
	}

	mode := handle.mode
	if handle.degraded {
		mode = progressStyleAssistantStatus
	}

	if mode == progressStyleStream {
		if err := p.updateNativeStream(ctx, handle, items, payload.State, payload.Truncated); err != nil {
			slog.Warn("slack: stream progress failed, degrading to assistant status", "error", err)
			handle.degraded = true
			if stopErr := p.stopNativeStream(ctx, handle, core.ProgressCardStateFailed); stopErr != nil {
				slog.Debug("slack: stop stream on degrade failed", "error", stopErr)
			}
		}
	}
	return p.updateNativeAssistantStatus(ctx, handle.replyCtx, items)
}

func (p *Platform) finishNativeProgress(ctx context.Context, handle *nativeProgressHandle, state core.ProgressCardState) error {
	if handle == nil {
		return nil
	}
	if handle.mode == progressStyleStream && !handle.degraded {
		if err := p.stopNativeStream(ctx, handle, state); err != nil {
			slog.Warn("slack: stop progress stream failed", "error", err)
		}
	}
	return p.clearNativeAssistantStatus(ctx, handle.replyCtx)
}

func (p *Platform) updateNativeAssistantStatus(ctx context.Context, replyCtx replyContext, items []core.ProgressCardEntry) error {
	status, loading := assistantStatusFromItems(items)
	return p.setAssistantStatus(ctx, replyCtx, status, loading)
}

func (p *Platform) setAssistantStatus(ctx context.Context, replyCtx replyContext, status string, loadingMessages []string) error {
	if p.client == nil {
		return fmt.Errorf("slack: client not initialized")
	}
	channel, threadTS, ok := p.threadTarget(replyCtx)
	if !ok {
		return fmt.Errorf("slack: missing thread context for assistant status")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "is thinking..."
	}
	params := slack.AssistantThreadsSetStatusParameters{
		ChannelID: channel,
		ThreadTS:  threadTS,
		Status:    status,
	}
	if len(loadingMessages) > 0 {
		params.LoadingMessages = loadingMessages
	}
	apiCtx, cancel := p.withSlackAPITimeout(ctx)
	err := p.client.SetAssistantThreadsStatusContext(apiCtx, params)
	cancel()
	if err != nil {
		return fmt.Errorf("slack: set assistant status: %w", err)
	}
	p.ensureStatusKeepalive(channel, threadTS, status, loadingMessages)
	return nil
}

func (p *Platform) clearNativeAssistantStatus(ctx context.Context, replyCtx replyContext) error {
	if p.client == nil {
		return nil
	}
	channel, threadTS, ok := p.threadTarget(replyCtx)
	if !ok {
		return nil
	}
	p.stopStatusKeepalive(slackThreadKey(channel, threadTS))
	apiCtx, cancel := p.withSlackAPITimeout(ctx)
	err := p.client.SetAssistantThreadsStatusContext(apiCtx, slack.AssistantThreadsSetStatusParameters{
		ChannelID: channel,
		ThreadTS:  threadTS,
		Status:    "",
	})
	cancel()
	if err != nil {
		return fmt.Errorf("slack: clear assistant status: %w", err)
	}
	return nil
}

func (p *Platform) updateNativeStream(ctx context.Context, handle *nativeProgressHandle, items []core.ProgressCardEntry, state core.ProgressCardState, truncated bool) error {
	if p.client == nil {
		return fmt.Errorf("slack: client not initialized")
	}
	if handle.stream == nil {
		channel, threadTS, ok := p.threadTarget(handle.replyCtx)
		if !ok {
			return fmt.Errorf("slack: missing thread context for progress stream")
		}
		title := "Agent"
		if len(items) > 0 && strings.TrimSpace(items[0].Tool) != "" {
			title = strings.TrimSpace(items[0].Tool)
		}
		apiCtx, cancel := p.withSlackAPITimeout(ctx)
		_, streamTS, err := p.client.StartStreamContext(apiCtx, channel,
			slack.MsgOptionTS(threadTS),
			slack.MsgOptionTaskDisplayMode(slack.TaskDisplayModeTimeline),
			slack.MsgOptionChunks(slack.NewPlanUpdateChunk(title+" is working")),
		)
		cancel()
		if err != nil {
			return fmt.Errorf("slack: start progress stream: %w", err)
		}
		handle.stream = &streamProgressHandle{
			channel:  channel,
			threadTS: threadTS,
			streamTS: streamTS,
		}
		handle.stream.startKeepalive(p, ctx)
	}

	h := handle.stream
	chunks, nextCount := streamDeltaChunks(items, state, truncated, h.lastSentCount, h.wasTruncated)
	if len(chunks) == 0 {
		return nil
	}
	apiCtx, cancel := p.withSlackAPITimeout(ctx)
	_, _, err := p.client.AppendStreamContext(apiCtx, h.channel, h.streamTS, slack.MsgOptionChunks(chunks...))
	cancel()
	if err != nil {
		return fmt.Errorf("slack: append progress stream: %w", err)
	}
	h.lastSentCount = nextCount
	h.wasTruncated = truncated
	return nil
}

func (p *Platform) stopNativeStream(ctx context.Context, handle *nativeProgressHandle, state core.ProgressCardState) error {
	if p.client == nil || handle == nil || handle.stream == nil {
		return nil
	}
	h := handle.stream
	if h.cancelKeep != nil {
		h.cancelKeep()
	}
	if h.streamTS == "" {
		handle.stream = nil
		return nil
	}
	var chunks []slack.StreamChunk
	switch state {
	case core.ProgressCardStateFailed:
		chunks = []slack.StreamChunk{slack.NewMarkdownTextChunk("_Progress stopped due to an error._")}
	default:
		chunks = []slack.StreamChunk{slack.NewMarkdownTextChunk("_Progress complete._")}
	}
	apiCtx, cancel := p.withSlackAPITimeout(ctx)
	_, _, err := p.client.StopStreamContext(apiCtx, h.channel, h.streamTS, slack.MsgOptionChunks(chunks...))
	cancel()
	handle.stream = nil
	if err != nil {
		return fmt.Errorf("slack: stop progress stream: %w", err)
	}
	return nil
}

func streamDeltaChunks(items []core.ProgressCardEntry, state core.ProgressCardState, truncated bool, lastSent int, wasTruncated bool) ([]slack.StreamChunk, int) {
	if len(items) == 0 {
		return nil, lastSent
	}
	reset := lastSent == 0 || (truncated && !wasTruncated) || (truncated && lastSent > len(items))
	start := lastSent
	if reset {
		start = 0
	}
	if start >= len(items) {
		chunk := progressEntryToTaskChunk(items[len(items)-1], len(items))
		return []slack.StreamChunk{chunk}, len(items)
	}

	var chunks []slack.StreamChunk
	if start == 0 {
		chunks = append(chunks, slack.NewPlanUpdateChunk(streamPlanTitle(state, truncated)))
	}
	for i := start; i < len(items); i++ {
		chunks = append(chunks, progressEntryToTaskChunk(items[i], i+1))
	}
	return chunks, len(items)
}

func streamPlanTitle(state core.ProgressCardState, truncated bool) string {
	title := "Working..."
	switch state {
	case core.ProgressCardStateFailed:
		title = "Stopped"
	case core.ProgressCardStateCompleted:
		title = "Completed"
	}
	if truncated {
		title += " (latest steps)"
	}
	return title
}

func progressEntryToTaskChunk(item core.ProgressCardEntry, seq int) slack.TaskUpdateChunk {
	title := strings.TrimSpace(item.Tool)
	if title == "" {
		title = strings.TrimSpace(item.Text)
	}
	if title == "" {
		title = "Step"
	}
	id := fmt.Sprintf("p%d", seq)
	chunk := slack.NewTaskUpdateChunk(id, trimSlackStreamText(title, 120))
	chunk.Status = taskStatusForEntry(item)
	if details := trimSlackStreamText(strings.TrimSpace(item.Text), 256); details != "" && details != chunk.Title {
		chunk.Details = details
	}
	if item.Kind == core.ProgressEntryToolResult {
		chunk.Output = trimSlackStreamText(strings.TrimSpace(item.Text), 256)
	}
	return chunk
}

func taskStatusForEntry(item core.ProgressCardEntry) slack.TaskCardStatus {
	switch item.Kind {
	case core.ProgressEntryToolResult:
		if item.Success != nil && !*item.Success {
			return slack.TaskCardStatusError
		}
		return slack.TaskCardStatusComplete
	case core.ProgressEntryError:
		return slack.TaskCardStatusError
	case core.ProgressEntryToolUse:
		return slack.TaskCardStatusInProgress
	default:
		return slack.TaskCardStatusInProgress
	}
}

func assistantStatusFromItems(items []core.ProgressCardEntry) (status string, loading []string) {
	if len(items) == 0 {
		return "is thinking...", nil
	}
	latest := items[len(items)-1]
	status = assistantStatusPhrase(latest)
	for i := len(items) - 1; i >= 0 && len(loading) < 3; i-- {
		entry := assistantStatusLoadingLine(items[i])
		if entry == "" {
			continue
		}
		loading = append([]string{entry}, loading...)
	}
	return status, loading
}

func assistantStatusPhrase(item core.ProgressCardEntry) string {
	switch item.Kind {
	case core.ProgressEntryThinking:
		return "is thinking..."
	case core.ProgressEntryToolUse:
		if item.Tool != "" {
			return "is running " + item.Tool + "..."
		}
		return "is running a tool..."
	case core.ProgressEntryToolResult:
		if item.Tool != "" {
			return "is finishing " + item.Tool + "..."
		}
		return "is processing results..."
	case core.ProgressEntryError:
		return "hit an error..."
	default:
		return "is working..."
	}
}

func assistantStatusLoadingLine(item core.ProgressCardEntry) string {
	line := strings.TrimSpace(item.Text)
	if line == "" {
		line = strings.TrimSpace(item.Tool)
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "💭"))
	line = strings.TrimSpace(strings.TrimPrefix(line, "🔧"))
	line = strings.TrimSpace(strings.TrimPrefix(line, "🧾"))
	line = strings.TrimSpace(strings.TrimPrefix(line, "❌"))
	if line == "" {
		return ""
	}
	if len([]rune(line)) > 120 {
		line = string([]rune(line)[:117]) + "..."
	}
	return line
}

func trimSlackStreamText(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	rs := []rune(s)
	return string(rs[:maxRunes-1]) + "…"
}

func (p *Platform) withSlackAPITimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, slackProgressAPITimeout)
}

func (p *Platform) ensureStatusKeepalive(channel, threadTS, status string, loading []string) {
	key := slackThreadKey(channel, threadTS)
	p.statusKeepaliveMu.Lock()
	if p.statusKeepalive == nil {
		p.statusKeepalive = make(map[string]context.CancelFunc)
	}
	if cancel, ok := p.statusKeepalive[key]; ok {
		cancel()
	}
	keepCtx, cancel := context.WithCancel(context.Background())
	p.statusKeepalive[key] = cancel
	p.statusKeepaliveMu.Unlock()

	loadingCopy := append([]string(nil), loading...)
	go func() {
		ticker := time.NewTicker(slackStatusKeepalive)
		defer ticker.Stop()
		for {
			select {
			case <-keepCtx.Done():
				return
			case <-ticker.C:
				apiCtx, apiCancel := p.withSlackAPITimeout(context.Background())
				params := slack.AssistantThreadsSetStatusParameters{
					ChannelID: channel,
					ThreadTS:  threadTS,
					Status:    status,
				}
				if len(loadingCopy) > 0 {
					params.LoadingMessages = loadingCopy
				}
				if err := p.client.SetAssistantThreadsStatusContext(apiCtx, params); err != nil {
					slog.Debug("slack: assistant status keepalive failed", "error", err)
				}
				apiCancel()
			}
		}
	}()
}

func (p *Platform) stopStatusKeepalive(key string) {
	p.statusKeepaliveMu.Lock()
	cancel, ok := p.statusKeepalive[key]
	if ok {
		delete(p.statusKeepalive, key)
	}
	p.statusKeepaliveMu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func (h *streamProgressHandle) startKeepalive(p *Platform, parent context.Context) {
	if p == nil || p.client == nil || h.streamTS == "" {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	keepCtx, cancel := context.WithCancel(parent)
	h.cancelKeep = cancel
	go func() {
		ticker := time.NewTicker(slackProgressKeepalive)
		defer ticker.Stop()
		for {
			select {
			case <-keepCtx.Done():
				return
			case <-ticker.C:
				apiCtx, apiCancel := p.withSlackAPITimeout(context.Background())
				_, _, err := p.client.AppendStreamContext(apiCtx, h.channel, h.streamTS,
					slack.MsgOptionChunks(slack.NewPlanUpdateChunk("Still working…")),
				)
				apiCancel()
				if err != nil {
					slog.Debug("slack: progress stream keepalive failed", "error", err)
				}
			}
		}
	}()
}

var (
	_ core.ProgressStyleProvider      = (*progressPlatform)(nil)
	_ core.ProgressCardPayloadSupport = (*progressPlatform)(nil)
	_ core.ProgressUpdateThrottler    = (*progressPlatform)(nil)
	_ core.PreviewStarter             = (*Platform)(nil)
	_ core.MessageUpdater             = (*Platform)(nil)
)

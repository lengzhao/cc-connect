package chatapi

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// logSSELifecycle emits an Info SSE connection lifecycle log (one line per event).
// Events: start, disconnect, resume, resume_miss, resume_rejected, aborted,
// end, discarded.
// Extra attrs may include reason, replay_event, terminal, error, answer_bytes.
func logSSELifecycle(event string, run *runState, attrs ...any) {
	if run == nil {
		return
	}
	args := []any{
		"run_id", run.id,
		"user", run.user,
		"conversation_id", run.conversationID,
		"channel", run.channelKey,
	}
	args = append(args, attrs...)
	slogSSELifecycle(event, args...)
}

// logSSELifecyclePartial logs lifecycle events when no runState is available (e.g. empty resume).
func logSSELifecyclePartial(event, runID, user, conversationID string, attrs ...any) {
	args := []any{"run_id", runID, "user", user}
	if conversationID != "" {
		args = append(args, "conversation_id", conversationID)
	}
	args = append(args, attrs...)
	slogSSELifecycle(event, args...)
}

func slogSSELifecycle(event string, args ...any) {
	slog.Log(context.Background(), slog.LevelInfo, "chat-api: sse "+event, args...)
}

// logSSEEnd records a terminal event handed to an attached SSE stream, i.e. one
// the client is still connected to receive. Latency stages:
//
//	e2e_ms         request receipt -> terminal written
//	first_delta_ms request receipt -> first SSE event reached the client
//	               (dispatch, busy queue, hooks, session spawn, agent TTFT)
//	stream_ms      first event -> terminal (pure streaming output phase)
func logSSEEnd(run *runState, result pendingResult) {
	terminal := terminalName(result)
	attrs := append(latencyStageAttrs(run), "terminal", terminal)
	if errText := terminalErrorText(result); errText != "" {
		attrs = append(attrs, "error", errText)
	}
	logSSELifecycle("end", run, attrs...)
}

// logSSEDiscarded records a terminal event that reached no client: the run had
// already been detached (client_gone or a write error), so serveRunSSE was no
// longer reading run.done and nothing was written to the wire.
//
// This case used to be logged as "sse end terminal=message_end", identical to a
// successful delivery. A dashboard counting terminal events therefore showed
// every discarded answer as delivered, which is why answers silently going
// missing went unnoticed in production. answer_bytes is the size of the answer
// that was thrown away, so the loss is measurable.
func logSSEDiscarded(run *runState, result pendingResult) {
	terminal := terminalName(result)
	attrs := append(latencyStageAttrs(run), "terminal", terminal, "answer_bytes", len(result.answer))
	if errText := terminalErrorText(result); errText != "" {
		attrs = append(attrs, "error", errText)
	}
	logSSELifecycle("discarded", run, attrs...)
}

// latencyStageAttrs reports how long the run took end to end and where the
// time went: everything before the first flushed SSE event (dispatch, queue,
// hooks, session spawn, agent TTFT) vs the streaming phase after it.
func latencyStageAttrs(run *runState) []any {
	if run.created.IsZero() {
		return nil
	}
	now := time.Now()
	attrs := []any{"e2e_ms", now.Sub(run.created).Milliseconds()}
	if ff := run.firstFlushedAtTime(); !ff.IsZero() {
		attrs = append(attrs,
			"first_delta_ms", ff.Sub(run.created).Milliseconds(),
			"stream_ms", now.Sub(ff).Milliseconds(),
		)
	}
	return attrs
}

func terminalName(result pendingResult) string {
	switch {
	case result.queued:
		return "message_queued"
	case result.queueFull, result.userCanceled, result.interactionTimedOut, result.err != nil:
		return "error"
	default:
		return "message_end"
	}
}

func terminalErrorText(result pendingResult) string {
	switch {
	case result.userCanceled:
		return errUserCanceled.Error()
	case result.interactionTimedOut || errors.Is(result.err, errInteractionTimedOut):
		return errInteractionTimedOut.Error()
	case result.queueFull:
		return result.errMsg
	case result.err != nil:
		return result.err.Error()
	default:
		return ""
	}
}

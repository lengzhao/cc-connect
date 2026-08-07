package chatapi

import (
	"context"
	"errors"
	"log/slog"
)

// logSSELifecycle emits an Info SSE connection lifecycle log (one line per event).
// Events: start, disconnect, resume, resume_miss, resume_rejected, end.
// Extra attrs may include reason, replay_event, terminal, error.
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

func logSSEEnd(run *runState, result pendingResult) {
	terminal := terminalName(result)
	if errText := terminalErrorText(result); errText != "" {
		logSSELifecycle("end", run, "terminal", terminal, "error", errText)
	} else {
		logSSELifecycle("end", run, "terminal", terminal)
	}
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

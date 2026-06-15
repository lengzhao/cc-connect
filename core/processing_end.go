package core

import "log/slog"

func builtinCommandCompletesSynchronously(cmdID string) bool {
	switch cmdID {
	case "shell", "diff", "compress":
		return false
	default:
		return cmdID != ""
	}
}

func (e *Engine) notifyProcessingEnd(p Platform, replyCtx any, event ProcessingEndEvent) {
	notifier, ok := p.(ProcessingEndNotifier)
	if !ok {
		return
	}
	if err := notifier.OnProcessingEnd(e.ctx, replyCtx, event); err != nil {
		slog.Warn("processing end notification failed", "platform", p.Name(), "kind", event.Kind, "error", err)
	}
}

package chatapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// BindIdleAgentSessionCloser wires the engine's idle closer into this platform.
func (p *Platform) BindIdleAgentSessionCloser(c core.IdleAgentSessionCloser) {
	p.idleCloserMu.Lock()
	defer p.idleCloserMu.Unlock()
	p.idleCloser = c
}

func (p *Platform) getIdleCloser() core.IdleAgentSessionCloser {
	p.idleCloserMu.RLock()
	defer p.idleCloserMu.RUnlock()
	return p.idleCloser
}

func (p *Platform) handleCloseIdleAgentSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}

	closer := p.getIdleCloser()
	if closer == nil {
		writeErr(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}

	result := closer.CloseIdleAgentSessions()

	user := strings.TrimSpace(r.Header.Get(p.userHeader))
	channel := strings.TrimSpace(r.Header.Get(p.channelHeader))
	attrs := []any{
		"closed", result.Closed,
		"skipped", result.Skipped,
	}
	if user != "" {
		attrs = append(attrs, "user", user)
	}
	if channel != "" {
		attrs = append(attrs, "channel", channel)
	}
	slog.Info("chat-api: close idle agent sessions", attrs...)

	closedKeys := result.ClosedSessionKeys
	if closedKeys == nil {
		closedKeys = []string{}
	}
	skippedKeys := result.SkippedSessionKeys
	if skippedKeys == nil {
		skippedKeys = []string{}
	}
	writeOK(w, http.StatusOK, map[string]any{
		"closed":               result.Closed,
		"skipped":              result.Skipped,
		"closed_session_keys":  closedKeys,
		"skipped_session_keys": skippedKeys,
	})
}

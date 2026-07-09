package chatapi

import (
	"net/http"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func (p *Platform) handleRunRoutes(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, p.path+"runs/")
	if sub == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	parts := strings.Split(sub, "/")
	if len(parts) != 2 || parts[1] != "cancel" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	p.handleCancelRun(w, r, parts[0])
}

func (p *Platform) handleCancelRun(w http.ResponseWriter, r *http.Request, runID string) {
	user, ok := p.resolveUser(w, r, true)
	if !ok {
		return
	}
	run := p.pending.get(runID)
	if run == nil || run.user != user {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	p.dispatchStop(run.sessionKey, user, run.channelKey, run.replyContext())
	if !p.pending.cancelUser(runID) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeOK(w, http.StatusOK, map[string]string{"result": "success"})
}

func (p *Platform) dispatchStop(sessionKey, user, channelKey string, replyCtx replyContext) {
	handler := p.getHandler()
	if handler == nil {
		return
	}
	go handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   p.Name(),
		ChannelID:  channelKey,
		ChannelKey: channelKey,
		UserID:     user,
		UserName:   user,
		Content:    "/stop",
		ReplyCtx:   replyCtx,
	})
}

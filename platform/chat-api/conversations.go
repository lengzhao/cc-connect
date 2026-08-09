package chatapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (p *Platform) handleConversations(w http.ResponseWriter, r *http.Request) {
	channel, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	if p.sessionsOrReload() == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
		sessions := p.sessionsOrReload()
		conversations, hasMore, nextCursor, err := paginateConversations(sessions.ListSessions(sessionKeyForChannel(channel)), cursor, limit)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		data := map[string]any{
			"limit":         clampLimit(limit),
			"has_more":      hasMore,
			"conversations": conversations,
		}
		if nextCursor != "" {
			data["next_cursor"] = nextCursor
		}
		writeOK(w, http.StatusOK, data)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
	}
}

func (p *Platform) handleConversationSub(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, p.path+"conversations/")
	if sub == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	parts := strings.Split(sub, "/")
	conversationID := parts[0]
	if conversationID == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	if len(parts) == 2 && parts[1] == "messages" {
		p.handleConversationMessages(w, r, conversationID)
		return
	}
	if len(parts) == 3 && parts[1] == "name" && parts[2] == "generate" {
		p.handleGenerateConversationName(w, r, conversationID)
		return
	}
	if len(parts) != 1 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		p.handleGetConversation(w, r, conversationID)
	case http.MethodPatch:
		p.handlePatchConversation(w, r, conversationID)
	case http.MethodDelete:
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
	default:
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
	}
}

func (p *Platform) handleGetConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	channel, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	sessions := p.sessionsOrReload()
	if sessions == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s := p.findConversationInChannel(sessions, channel, conversationID)
	if s == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeOK(w, http.StatusOK, toConversationView(s))
}

func (p *Platform) handlePatchConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	channel, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	if _, ok := p.resolveUser(w, r, true); !ok {
		return
	}
	if p.sessionsOrReload() == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	sessions := p.sessionsOrReload()
	s := p.findConversationInChannel(sessions, channel, conversationID)
	if s == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	s.SetName(name)
	sessions.Save()
	writeOK(w, http.StatusOK, map[string]any{
		"id":         s.ID,
		"name":       s.GetName(),
		"updated_at": s.GetUpdatedAt().Unix(),
	})
}

func (p *Platform) handleConversationMessages(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	channel, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	sessions := p.sessionsOrReload()
	if sessions == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s := p.findConversationInChannel(sessions, channel, conversationID)
	if s == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursor != "" {
		cursorConv, _, err := parseMessageCursor(cursor)
		if err != nil || cursorConv != conversationID {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	items := historyMessages(conversationID, s.GetHistory(0))
	page, hasMore, nextCursor, err := paginateMessages(items, cursor, limit)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	messages := make([]map[string]any, len(page))
	for i, m := range page {
		messages[i] = historyMessageToAPI(m)
	}
	data := map[string]any{
		"limit":    clampLimit(limit),
		"has_more": hasMore,
		"messages": messages,
	}
	if nextCursor != "" {
		data["next_cursor"] = nextCursor
	}
	writeOK(w, http.StatusOK, data)
}

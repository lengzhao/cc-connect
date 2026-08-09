package chatapi

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

const previewMaxRunes = 200

type historyMessage struct {
	ID        string
	Role      string
	Content   string
	UserID    string
	UserName  string
	CreatedAt int64
	Index     int
}

func messageID(conversationID string, entryIndex int) string {
	return fmt.Sprintf("%s:%d", conversationID, entryIndex)
}

func parseMessageCursor(cursor string) (conversationID string, entryIndex int, err error) {
	if cursor == "" {
		return "", -1, nil
	}
	idx := strings.LastIndex(cursor, ":")
	if idx <= 0 || idx >= len(cursor)-1 {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	conversationID = cursor[:idx]
	entryIndex, err = strconv.Atoi(cursor[idx+1:])
	if err != nil || entryIndex < 0 {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	return conversationID, entryIndex, nil
}

// historyMessages maps session history entries 1:1 for GET /messages.
func historyMessages(conversationID string, entries []core.HistoryEntry) []historyMessage {
	out := make([]historyMessage, len(entries))
	for i, e := range entries {
		out[i] = historyMessage{
			ID:        messageID(conversationID, i),
			Role:      e.Role,
			Content:   e.Content,
			UserID:    e.UserID,
			UserName:  e.UserName,
			CreatedAt: e.Timestamp.Unix(),
			Index:     i,
		}
	}
	return out
}

func historyMessageToAPI(m historyMessage) map[string]any {
	msg := map[string]any{
		"id":         m.ID,
		"role":       m.Role,
		"content":    m.Content,
		"created_at": m.CreatedAt,
	}
	if m.UserID != "" {
		msg["user_id"] = m.UserID
	}
	if m.UserName != "" {
		msg["user_name"] = m.UserName
	}
	// Legacy query/answer fields for clients that still expect turn-shaped rows.
	switch m.Role {
	case "user":
		msg["query"] = m.Content
	case "assistant":
		msg["answer"] = m.Content
	}
	return msg
}

func countCompletedTurns(entries []core.HistoryEntry) int {
	n := 0
	for i := 0; i < len(entries); {
		if entries[i].Role != "user" {
			i++
			continue
		}
		i++
		if i >= len(entries) || entries[i].Role != "assistant" {
			break
		}
		n++
		i++
	}
	return n
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}

func lastMessagePreview(entries []core.HistoryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	last := entries[len(entries)-1].Content
	return truncateRunes(last, previewMaxRunes)
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

type conversationView struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	LastMessagePreview string `json:"last_message_preview,omitempty"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

func toConversationView(s *core.Session) conversationView {
	hist := s.GetHistory(0)
	view := conversationView{
		ID:        s.ID,
		Name:      s.GetName(),
		CreatedAt: s.CreatedAt.Unix(),
		UpdatedAt: s.GetUpdatedAt().Unix(),
	}
	if preview := lastMessagePreview(hist); preview != "" {
		view.LastMessagePreview = preview
	}
	return view
}

func sortSessionsByUpdatedAt(sessions []*core.Session) {
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].GetUpdatedAt().After(sessions[j].GetUpdatedAt())
	})
}

func paginateConversations(sessions []*core.Session, cursor string, limit int) ([]conversationView, bool, string, error) {
	sortSessionsByUpdatedAt(sessions)
	limit = clampLimit(limit)

	start := 0
	if cursor != "" {
		found := false
		for i, s := range sessions {
			if s.ID == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, false, "", fmt.Errorf("not found")
		}
	}

	end := start + limit
	hasMore := end < len(sessions)
	if end > len(sessions) {
		end = len(sessions)
	}
	page := sessions[start:end]
	out := make([]conversationView, len(page))
	for i, s := range page {
		out[i] = toConversationView(s)
	}
	var nextCursor string
	if hasMore && len(page) > 0 {
		nextCursor = page[len(page)-1].ID
	}
	return out, hasMore, nextCursor, nil
}

func paginateMessages(items []historyMessage, cursor string, limit int) ([]historyMessage, bool, string, error) {
	limit = clampLimit(limit)
	// Newest first.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}

	start := 0
	if cursor != "" {
		_, entryIndex, err := parseMessageCursor(cursor)
		if err != nil {
			return nil, false, "", err
		}
		found := false
		for i, m := range items {
			if m.Index == entryIndex {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, false, "", fmt.Errorf("not found")
		}
	}

	end := start + limit
	hasMore := end < len(items)
	if end > len(items) {
		end = len(items)
	}
	page := items[start:end]
	var nextCursor string
	if hasMore && len(page) > 0 {
		nextCursor = page[len(page)-1].ID
	}
	return page, hasMore, nextCursor, nil
}

func (p *Platform) sessionInChannel(sessions *core.SessionManager, channel, conversationID string) bool {
	if sessions == nil {
		return false
	}
	key := sessionKeyForChannel(channel)
	for _, s := range sessions.ListSessions(key) {
		if s.ID == conversationID {
			return true
		}
	}
	return false
}

func (p *Platform) findConversationInChannel(sessions *core.SessionManager, channel, conversationID string) *core.Session {
	if !p.sessionInChannel(sessions, channel, conversationID) {
		return nil
	}
	return sessions.FindByID(conversationID)
}

// autoNameFromQuery truncates the first user query to 32 runes for session title.
func autoNameFromQuery(query string) string {
	name := strings.TrimSpace(query)
	if name == "" {
		return "default"
	}
	return truncateRunes(name, 32)
}

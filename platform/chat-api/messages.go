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

type pairedMessage struct {
	ID        string
	Query     string
	Answer    string
	UserID    string
	UserName  string
	CreatedAt int64
	TurnIndex int
}

func messageID(conversationID string, turnIndex int) string {
	return fmt.Sprintf("%s:%d", conversationID, turnIndex)
}

func parseMessageCursor(cursor string) (conversationID string, turnIndex int, err error) {
	if cursor == "" {
		return "", -1, nil
	}
	idx := strings.LastIndex(cursor, ":")
	if idx <= 0 || idx >= len(cursor)-1 {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	conversationID = cursor[:idx]
	turnIndex, err = strconv.Atoi(cursor[idx+1:])
	if err != nil || turnIndex < 0 {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	return conversationID, turnIndex, nil
}

func pairHistory(conversationID string, entries []core.HistoryEntry) []pairedMessage {
	var out []pairedMessage
	turn := 0
	for i := 0; i < len(entries); {
		if entries[i].Role != "user" {
			i++
			continue
		}
		user := entries[i]
		i++
		if i >= len(entries) || entries[i].Role != "assistant" {
			continue
		}
		assistant := entries[i]
		i++
		out = append(out, pairedMessage{
			ID:        messageID(conversationID, turn),
			Query:     user.Content,
			Answer:    assistant.Content,
			UserID:    user.UserID,
			UserName:  user.UserName,
			CreatedAt: user.Timestamp.Unix(),
			TurnIndex: turn,
		})
		turn++
	}
	return out
}

func countCompletedTurns(entries []core.HistoryEntry) int {
	return len(pairHistory("", entries))
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

func paginateMessages(pairs []pairedMessage, cursor string, limit int) ([]pairedMessage, bool, string, error) {
	limit = clampLimit(limit)
	// Newest first.
	for i, j := 0, len(pairs)-1; i < j; i, j = i+1, j-1 {
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}

	start := 0
	if cursor != "" {
		_, turnIndex, err := parseMessageCursor(cursor)
		if err != nil {
			return nil, false, "", err
		}
		found := false
		for i, m := range pairs {
			if m.TurnIndex == turnIndex {
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
	hasMore := end < len(pairs)
	if end > len(pairs) {
		end = len(pairs)
	}
	page := pairs[start:end]
	var nextCursor string
	if hasMore && len(page) > 0 {
		nextCursor = page[len(page)-1].ID
	}
	return page, hasMore, nextCursor, nil
}

func (p *Platform) sessionOwnedByUser(sessions *core.SessionManager, user, conversationID string) bool {
	if sessions == nil {
		return false
	}
	key := sessionKeyForUser(user)
	for _, s := range sessions.ListSessions(key) {
		if s.ID == conversationID {
			return true
		}
	}
	return false
}

func (p *Platform) findConversation(sessions *core.SessionManager, conversationID string) *core.Session {
	if sessions == nil {
		return nil
	}
	return sessions.FindByID(conversationID)
}

func (p *Platform) findOwnedConversation(sessions *core.SessionManager, user, conversationID string) *core.Session {
	if !p.sessionOwnedByUser(sessions, user, conversationID) {
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

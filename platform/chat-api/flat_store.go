package chatapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	flatConversationsFile  = "conversations.json"
	flatLegacySessionsFile = "sessions.json"
	flatMessageExt         = ".jsonl"
	flatIndexVersion       = 1
)

type flatConversationMeta struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	AgentSessionID      string    `json:"agent_session_id,omitempty"`
	AgentType           string    `json:"agent_type,omitempty"`
	PastAgentSessionIDs []string  `json:"past_agent_session_ids,omitempty"`
	ActiveProvider      string    `json:"active_provider,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	LastUserActivity    time.Time `json:"last_user_activity,omitempty"`
}

type flatUserIndex struct {
	UserID        string                           `json:"user_id,omitempty"`
	Version       int                              `json:"version"`
	Counter       int64                            `json:"counter"`
	ActiveSession map[string]string                `json:"active_session"`
	UserSessions  map[string][]string              `json:"user_sessions"`
	Conversations map[string]*flatConversationMeta `json:"conversations"`
	SessionNames  map[string]string                `json:"session_names,omitempty"`
	UserMeta      map[string]*core.UserMeta        `json:"user_meta,omitempty"`
	LegacyData    bool                             `json:"legacy_data,omitempty"`
}

type flatUserStore struct {
	userDir       string
	userID        string
	historySynced map[string]int
}

func newFlatUserStore(userDir, userID string) *flatUserStore {
	return &flatUserStore{
		userDir:       userDir,
		userID:        userID,
		historySynced: make(map[string]int),
	}
}

func newSessionManagerForFlatUser(userDir, userID string) *core.SessionManager {
	sm := core.NewSessionManager("")
	store := newFlatUserStore(userDir, userID)
	if err := store.loadInto(sm); err != nil {
		slog.Warn("chat-api: flat store load failed", "dir", userDir, "error", err)
	}
	sm.SetSaveHook(func(_ *core.SessionManager, snap core.SessionSnapshot) {
		store.syncFromSnapshot(snap)
	})
	return sm
}

func (f *flatUserStore) messagePath(conversationID string) string {
	return filepath.Join(f.userDir, conversationID+flatMessageExt)
}

func (f *flatUserStore) indexPath() string {
	return filepath.Join(f.userDir, flatConversationsFile)
}

func (f *flatUserStore) loadInto(sm *core.SessionManager) error {
	if err := os.MkdirAll(f.userDir, 0o755); err != nil {
		return err
	}
	if err := f.migrateLegacySessions(); err != nil {
		return err
	}

	index, err := readFlatUserIndex(f.indexPath())
	if err != nil {
		return err
	}
	if index == nil {
		return nil
	}

	snap := core.SessionSnapshot{
		Sessions:       make(map[string]*core.Session, len(index.Conversations)),
		ActiveSession:  index.ActiveSession,
		UserSessions:   index.UserSessions,
		SessionNames:   index.SessionNames,
		UserMeta:       index.UserMeta,
		Counter:        index.Counter,
		LegacyData:     index.LegacyData,
		Version:        1,
		PastIDTracking: true,
	}
	for id, meta := range index.Conversations {
		if meta == nil {
			continue
		}
		history, err := loadHistoryJSONL(f.messagePath(id))
		if err != nil {
			slog.Warn("chat-api: flat history load failed", "conversation", id, "error", err)
		}
		snap.Sessions[id] = meta.toSession(history)
		f.historySynced[id] = len(history)
	}
	sm.ImportSnapshot(snap)
	return nil
}

func (m *flatConversationMeta) toSession(history []core.HistoryEntry) *core.Session {
	if m == nil {
		return nil
	}
	return &core.Session{
		ID:                  m.ID,
		Name:                m.Name,
		AgentSessionID:      m.AgentSessionID,
		AgentType:           m.AgentType,
		PastAgentSessionIDs: append([]string(nil), m.PastAgentSessionIDs...),
		ActiveProvider:      m.ActiveProvider,
		History:             history,
		CreatedAt:           m.CreatedAt,
		UpdatedAt:           m.UpdatedAt,
		LastUserActivity:    m.LastUserActivity,
	}
}

func (f *flatUserStore) migrateLegacySessions() error {
	if _, err := os.Stat(f.indexPath()); err == nil {
		return nil
	}
	legacyPath := filepath.Join(f.userDir, flatLegacySessionsFile)
	if _, err := os.Stat(legacyPath); err != nil {
		return nil
	}
	legacySM := core.NewSessionManager(legacyPath)
	f.syncFromSnapshot(legacySM.ExportSnapshot())
	if err := os.Rename(legacyPath, legacyPath+".bak"); err != nil {
		slog.Warn("chat-api: flat migrate rename legacy failed", "path", legacyPath, "error", err)
	}
	slog.Info("chat-api: migrated legacy sessions.json to flat layout", "dir", f.userDir)
	return nil
}

func (f *flatUserStore) syncFromSnapshot(snap core.SessionSnapshot) {
	conversations := make(map[string]*flatConversationMeta, len(snap.Sessions))
	for id, s := range snap.Sessions {
		if s == nil {
			continue
		}
		conversations[id] = sessionToFlatMeta(s)
		if err := f.appendHistoryDelta(id, s.History); err != nil {
			slog.Error("chat-api: flat append history failed", "conversation", id, "error", err)
		}
	}

	for id := range f.historySynced {
		if _, ok := snap.Sessions[id]; !ok {
			_ = os.Remove(f.messagePath(id))
			delete(f.historySynced, id)
		}
	}

	index := flatUserIndex{
		UserID:        f.userID,
		Version:       flatIndexVersion,
		Counter:       snap.Counter,
		ActiveSession: snap.ActiveSession,
		UserSessions:  snap.UserSessions,
		Conversations: conversations,
		SessionNames:  snap.SessionNames,
		UserMeta:      snap.UserMeta,
		LegacyData:    snap.LegacyData,
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		slog.Error("chat-api: flat marshal index failed", "error", err)
		return
	}
	if err := os.MkdirAll(f.userDir, 0o755); err != nil {
		slog.Error("chat-api: flat mkdir failed", "error", err)
		return
	}
	if err := core.AtomicWriteFile(f.indexPath(), data, 0o644); err != nil {
		slog.Error("chat-api: flat write index failed", "path", f.indexPath(), "error", err)
	}
}

func sessionToFlatMeta(s *core.Session) *flatConversationMeta {
	if s == nil {
		return nil
	}
	agentSID := s.AgentSessionID
	if agentSID == core.ContinueSession {
		agentSID = ""
	}
	return &flatConversationMeta{
		ID:                  s.ID,
		Name:                s.Name,
		AgentSessionID:      agentSID,
		AgentType:           s.AgentType,
		PastAgentSessionIDs: append([]string(nil), s.PastAgentSessionIDs...),
		ActiveProvider:      s.ActiveProvider,
		CreatedAt:           s.CreatedAt,
		UpdatedAt:           s.UpdatedAt,
		LastUserActivity:    s.LastUserActivity,
	}
}

func (f *flatUserStore) appendHistoryDelta(conversationID string, history []core.HistoryEntry) error {
	synced := f.historySynced[conversationID]
	if synced > len(history) {
		if err := f.rewriteHistoryJSONL(conversationID, history); err != nil {
			return err
		}
		return nil
	}
	if synced >= len(history) {
		return nil
	}
	path := f.messagePath(conversationID)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	var buf bytes.Buffer
	for _, entry := range history[synced:] {
		line, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		f.historySynced[conversationID] = len(history)
		return nil
	}
	if _, err := file.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	f.historySynced[conversationID] = len(history)
	return nil
}

func (f *flatUserStore) rewriteHistoryJSONL(conversationID string, history []core.HistoryEntry) error {
	path := f.messagePath(conversationID)
	if len(history) == 0 {
		_ = os.Remove(path)
		f.historySynced[conversationID] = 0
		return nil
	}
	var buf bytes.Buffer
	for _, entry := range history {
		line, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := core.AtomicWriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	f.historySynced[conversationID] = len(history)
	return nil
}

func readFlatUserIndex(path string) (*flatUserIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var index flatUserIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	if index.Conversations == nil {
		index.Conversations = make(map[string]*flatConversationMeta)
	}
	return &index, nil
}

func loadHistoryJSONL(path string) ([]core.HistoryEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var out []core.HistoryEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry core.HistoryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return out, err
		}
		out = append(out, entry)
	}
	return out, scanner.Err()
}

func flatUserDirHasConversation(userDir, conversationID string) bool {
	index, err := readFlatUserIndex(filepath.Join(userDir, flatConversationsFile))
	if err != nil || index == nil {
		return false
	}
	_, ok := index.Conversations[conversationID]
	return ok
}

func flatUserDirUserID(userDir string) string {
	index, err := readFlatUserIndex(filepath.Join(userDir, flatConversationsFile))
	if err != nil || index == nil {
		return ""
	}
	return strings.TrimSpace(index.UserID)
}

func safePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	s = strings.ReplaceAll(s, string(filepath.Separator), "_")
	s = strings.ReplaceAll(s, ":", "_")
	if s == "." || s == ".." {
		return "_"
	}
	return s
}

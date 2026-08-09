package core

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sessionRecordVersion = 1

// SessionRecordEvent is one append-only JSONL record for channel-sharded storage.
type SessionRecordEvent struct {
	V                  int           `json:"v"`
	Op                 string        `json:"op"`
	TS                 int64         `json:"ts"`
	ConvID             string        `json:"conv_id,omitempty"`
	Name               string        `json:"name,omitempty"`
	CreatedBy          string        `json:"created_by,omitempty"`
	CreatedAt          int64         `json:"created_at,omitempty"`
	AgentSessionID     string        `json:"agent_session_id,omitempty"`
	AgentType          string        `json:"agent_type,omitempty"`
	ActiveProvider     string        `json:"active_provider,omitempty"`
	PastAgentSessionIDs []string     `json:"past_agent_session_ids,omitempty"`
	Entry              *HistoryEntry `json:"entry,omitempty"`
}

type jsonlChannelStore struct {
	dir string
}

func newJSONLChannelStore(dir string) *jsonlChannelStore {
	return &jsonlChannelStore{dir: dir}
}

func channelKeyHash(channelKey string) string {
	sum := sha256.Sum256([]byte(channelKey))
	return hex.EncodeToString(sum[:])
}

func (st *jsonlChannelStore) paths(channelKey string) (jsonlPath, metaPath string) {
	hash := channelKeyHash(channelKey)
	prefix := hash[:2]
	base := filepath.Join(st.dir, "channels", prefix, hash)
	return base + ".jsonl", base + ".meta.json"
}

func (st *jsonlChannelStore) ensureMeta(channelKey, metaPath string) error {
	if _, err := os.Stat(metaPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"channel_key": channelKey})
	if err != nil {
		return err
	}
	return AtomicWriteFile(metaPath, payload, 0o644)
}

func (st *jsonlChannelStore) append(channelKey string, events []SessionRecordEvent) error {
	if len(events) == 0 {
		return nil
	}
	jsonlPath, metaPath := st.paths(channelKey)
	if err := st.ensureMeta(channelKey, metaPath); err != nil {
		return fmt.Errorf("jsonl channel store meta: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, ev := range events {
		if ev.V == 0 {
			ev.V = sessionRecordVersion
		}
		if ev.TS == 0 {
			ev.TS = time.Now().Unix()
		}
		line, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return f.Sync()
}

func (st *jsonlChannelStore) loadAll() (map[string]*sessionSnapshot, error) {
	root := filepath.Join(st.dir, "channels")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make(map[string]*sessionSnapshot)
	for _, prefixEntry := range entries {
		if !prefixEntry.IsDir() {
			continue
		}
		prefixDir := filepath.Join(root, prefixEntry.Name())
		files, err := os.ReadDir(prefixDir)
		if err != nil {
			return nil, err
		}
		for _, fileEntry := range files {
			if fileEntry.IsDir() || !strings.HasSuffix(fileEntry.Name(), ".jsonl") {
				continue
			}
			jsonlPath := filepath.Join(prefixDir, fileEntry.Name())
			metaPath := strings.TrimSuffix(jsonlPath, ".jsonl") + ".meta.json"
			channelKey, err := readChannelMeta(metaPath)
			if err != nil {
				slog.Warn("jsonl channel store: skip shard without meta", "path", jsonlPath, "error", err)
				continue
			}
			snap, err := foldJSONLFile(jsonlPath, channelKey)
			if err != nil {
				return nil, fmt.Errorf("fold %s: %w", jsonlPath, err)
			}
			if existing, ok := out[channelKey]; ok {
				mergeSnapshots(existing, snap)
			} else {
				out[channelKey] = snap
			}
		}
	}
	return out, nil
}

func readChannelMeta(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var meta struct {
		ChannelKey string `json:"channel_key"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", err
	}
	if strings.TrimSpace(meta.ChannelKey) == "" {
		return "", fmt.Errorf("empty channel_key in %s", path)
	}
	return meta.ChannelKey, nil
}

func foldJSONLFile(path, channelKey string) (*sessionSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	snap := &sessionSnapshot{
		Sessions:      make(map[string]*Session),
		UserSessions:  map[string][]string{channelKey: {}},
		ActiveSession: make(map[string]string),
		Version:       snapshotVersion,
		PastIDTracking: true,
	}
	convOrder := make([]string, 0)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev SessionRecordEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			slog.Warn("jsonl channel store: skip bad line", "path", path, "error", err)
			continue
		}
		applySessionRecordEvent(snap, channelKey, &convOrder, &ev)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	snap.UserSessions[channelKey] = uniqueStrings(convOrder)
	return snap, nil
}

func applySessionRecordEvent(snap *sessionSnapshot, channelKey string, convOrder *[]string, ev *SessionRecordEvent) {
	if ev.ConvID == "" {
		return
	}
	s := snap.Sessions[ev.ConvID]
	switch ev.Op {
	case "conv_create":
		if s == nil {
			created := time.Unix(ev.CreatedAt, 0)
			if ev.CreatedAt == 0 {
				created = time.Unix(ev.TS, 0)
			}
			s = &Session{
				ID:        ev.ConvID,
				Name:      ev.Name,
				CreatedBy: ev.CreatedBy,
				CreatedAt: created,
				UpdatedAt: created,
			}
			if s.Name == "" {
				s.Name = "default"
			}
			snap.Sessions[ev.ConvID] = s
			*convOrder = append(*convOrder, ev.ConvID)
		}
	case "conv_rename":
		if s != nil && ev.Name != "" {
			s.Name = ev.Name
			s.UpdatedAt = time.Unix(ev.TS, 0)
		}
	case "conv_meta":
		if s == nil {
			return
		}
		s.AgentSessionID = ev.AgentSessionID
		s.AgentType = ev.AgentType
		s.ActiveProvider = ev.ActiveProvider
		if len(ev.PastAgentSessionIDs) > 0 {
			s.PastAgentSessionIDs = append([]string(nil), ev.PastAgentSessionIDs...)
		}
		s.UpdatedAt = time.Unix(ev.TS, 0)
	case "history_append":
		if s == nil {
			return
		}
		if ev.Entry != nil {
			entry := *ev.Entry
			if entry.Timestamp.IsZero() {
				entry.Timestamp = time.Unix(ev.TS, 0)
			}
			s.History = append(s.History, entry)
			s.UpdatedAt = entry.Timestamp
		}
	}
}

func mergeSnapshots(dst, src *sessionSnapshot) {
	for id, s := range src.Sessions {
		if existing, ok := dst.Sessions[id]; ok {
			if len(s.History) > len(existing.History) {
				existing.History = append([]HistoryEntry(nil), s.History...)
			}
			if s.UpdatedAt.After(existing.UpdatedAt) {
				existing.UpdatedAt = s.UpdatedAt
			}
			if s.Name != "" {
				existing.Name = s.Name
			}
			if s.AgentSessionID != "" {
				existing.AgentSessionID = s.AgentSessionID
			}
			continue
		}
		dst.Sessions[id] = s
	}
	for key, ids := range src.UserSessions {
		dst.UserSessions[key] = uniqueStrings(append(dst.UserSessions[key], ids...))
	}
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

type sessionPersistSnap struct {
	known       bool
	historyLen  int
	name        string
	agentSID    string
	agentType   string
	activeProv  string
	pastIDs     string
}

func pastIDsKey(ids []string) string {
	return strings.Join(ids, "\x00")
}

func (sm *SessionManager) appendJSONLChannelDeltas() {
	if sm.channelStore == nil || !sm.storeCfg.usesJSONLChannel() {
		return
	}
	if sm.persistSnap == nil {
		sm.persistSnap = make(map[string]sessionPersistSnap)
	}

	eventsByKey := make(map[string][]SessionRecordEvent)
	now := time.Now().Unix()

	for userKey, ids := range sm.userSessions {
		if !sm.storeCfg.matchesKey(userKey) {
			continue
		}
		for _, id := range ids {
			s := sm.sessions[id]
			if s == nil {
				continue
			}
			s.mu.Lock()
			snap := sessionPersistSnap{
				known:      true,
				historyLen: len(s.History),
				name:       s.Name,
				agentSID:   s.AgentSessionID,
				agentType:  s.AgentType,
				activeProv: s.ActiveProvider,
				pastIDs:    pastIDsKey(s.PastAgentSessionIDs),
			}
			prev, had := sm.persistSnap[id]
			if !had {
				eventsByKey[userKey] = append(eventsByKey[userKey], SessionRecordEvent{
					Op:        "conv_create",
					TS:        now,
					ConvID:    id,
					Name:      s.Name,
					CreatedBy: s.CreatedBy,
					CreatedAt: s.CreatedAt.Unix(),
				})
			}
			if had && prev.name != snap.name {
				eventsByKey[userKey] = append(eventsByKey[userKey], SessionRecordEvent{
					Op:     "conv_rename",
					TS:     now,
					ConvID: id,
					Name:   s.Name,
				})
			}
			metaChanged := !had ||
				prev.agentSID != snap.agentSID ||
				prev.agentType != snap.agentType ||
				prev.activeProv != snap.activeProv ||
				prev.pastIDs != snap.pastIDs
			if metaChanged && (snap.agentSID != "" || snap.agentType != "" || snap.activeProv != "" || snap.pastIDs != "") {
				eventsByKey[userKey] = append(eventsByKey[userKey], SessionRecordEvent{
					Op:                  "conv_meta",
					TS:                  now,
					ConvID:              id,
					AgentSessionID:      snap.agentSID,
					AgentType:           snap.agentType,
					ActiveProvider:      snap.activeProv,
					PastAgentSessionIDs: append([]string(nil), s.PastAgentSessionIDs...),
				})
			}
			start := 0
			if had {
				start = prev.historyLen
			}
			for i := start; i < len(s.History); i++ {
				entry := s.History[i]
				evEntry := entry
				eventsByKey[userKey] = append(eventsByKey[userKey], SessionRecordEvent{
					Op:     "history_append",
					TS:     now,
					ConvID: id,
					Entry:  &evEntry,
				})
			}
			s.mu.Unlock()
			sm.persistSnap[id] = snap
		}
	}

	for userKey, events := range eventsByKey {
		if err := sm.channelStore.append(userKey, events); err != nil {
			slog.Error("session: jsonl channel append failed", "channel_key", userKey, "error", err)
		}
	}
}

func (sm *SessionManager) loadJSONLChannels() {
	if sm.channelStore == nil {
		return
	}
	shards, err := sm.channelStore.loadAll()
	if err != nil {
		slog.Error("session: jsonl channel load failed", "error", err)
		return
	}
	for channelKey, shard := range shards {
		if shard == nil {
			continue
		}
		for id, s := range shard.Sessions {
			if s == nil {
				continue
			}
			s.stripContinueSessionSentinel()
			if existing, ok := sm.sessions[id]; ok {
				if len(s.History) > len(existing.History) {
					existing.mu.Lock()
					existing.History = append([]HistoryEntry(nil), s.History...)
					existing.UpdatedAt = s.UpdatedAt
					if s.Name != "" {
						existing.Name = s.Name
					}
					if s.CreatedBy != "" {
						existing.CreatedBy = s.CreatedBy
					}
					existing.mu.Unlock()
				}
				continue
			}
			sm.sessions[id] = s
		}
		if ids, ok := shard.UserSessions[channelKey]; ok {
			sm.userSessions[channelKey] = uniqueStrings(append(sm.userSessions[channelKey], ids...))
		}
	}
	if sm.persistSnap == nil {
		sm.persistSnap = make(map[string]sessionPersistSnap)
	}
	for id, s := range sm.sessions {
		s.mu.Lock()
		sm.persistSnap[id] = sessionPersistSnap{
			known:      true,
			historyLen: len(s.History),
			name:       s.Name,
			agentSID:   s.AgentSessionID,
			agentType:  s.AgentType,
			activeProv: s.ActiveProvider,
			pastIDs:    pastIDsKey(s.PastAgentSessionIDs),
		}
		s.mu.Unlock()
	}
}

func (sm *SessionManager) snapshotExcludingChannelKeys() sessionSnapshot {
	excluded := make(map[string]struct{})
	for userKey, ids := range sm.userSessions {
		if sm.storeCfg.matchesKey(userKey) {
			for _, id := range ids {
				excluded[id] = struct{}{}
			}
		}
	}
	snapSessions := make(map[string]*Session, len(sm.sessions))
	for id, s := range sm.sessions {
		if _, skip := excluded[id]; skip {
			continue
		}
		s.mu.Lock()
		agentSID := s.AgentSessionID
		if agentSID == ContinueSession {
			agentSID = ""
			s.AgentSessionID = ""
		}
		snapSessions[id] = &Session{
			ID:                  s.ID,
			Name:                s.Name,
			CreatedBy:           s.CreatedBy,
			AgentSessionID:      agentSID,
			AgentType:           s.AgentType,
			PastAgentSessionIDs: append([]string(nil), s.PastAgentSessionIDs...),
			ActiveProvider:      s.ActiveProvider,
			History:             append([]HistoryEntry(nil), s.History...),
			CreatedAt:           s.CreatedAt,
			UpdatedAt:           s.UpdatedAt,
			LastUserActivity:    s.LastUserActivity,
		}
		s.mu.Unlock()
	}
	userSessions := make(map[string][]string, len(sm.userSessions))
	activeSession := make(map[string]string, len(sm.activeSession))
	for userKey, ids := range sm.userSessions {
		if sm.storeCfg.matchesKey(userKey) {
			continue
		}
		userSessions[userKey] = append([]string(nil), ids...)
	}
	for userKey, id := range sm.activeSession {
		if sm.storeCfg.matchesKey(userKey) {
			continue
		}
		activeSession[userKey] = id
	}
	return sessionSnapshot{
		Sessions:       snapSessions,
		ActiveSession:  activeSession,
		UserSessions:   userSessions,
		Counter:        sm.counter,
		SessionNames:   sm.sessionNames,
		UserMeta:       sm.userMeta,
		PastIDTracking: true,
		LegacyData:     sm.legacyData,
		Version:        snapshotVersion,
	}
}

// SetStoreConfig enables JSONL channel sharding. Call before first Save or after
// NewSessionManager when loading from an existing store directory.
func (sm *SessionManager) SetStoreConfig(cfg SessionStoreConfig) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.storeCfg = cfg
	if cfg.usesJSONLChannel() {
		sm.channelStore = newJSONLChannelStore(cfg.Dir)
		sm.loadJSONLChannels()
	}
}

// FoldJSONLChannelFile loads one JSONL shard for tests and tooling.
func FoldJSONLChannelFile(path, channelKey string) (*sessionSnapshot, error) {
	return foldJSONLFile(path, channelKey)
}

// AppendJSONLChannelEvents appends events for tests and migration tooling.
func AppendJSONLChannelEvents(dir, channelKey string, events []SessionRecordEvent) error {
	st := newJSONLChannelStore(dir)
	return st.append(channelKey, events)
}

// LoadJSONLChannelStore reads all channel shards under dir.
func LoadJSONLChannelStore(dir string) (map[string]*sessionSnapshot, error) {
	return newJSONLChannelStore(dir).loadAll()
}

// CopySessionSnapshot merges a folded shard into SessionManager state (tests).
func (sm *SessionManager) mergeSnapshotShard(shard *sessionSnapshot) {
	if shard == nil {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for id, s := range shard.Sessions {
		sm.sessions[id] = s
	}
	for key, ids := range shard.UserSessions {
		sm.userSessions[key] = uniqueStrings(append(sm.userSessions[key], ids...))
	}
}

// DrainJSONLReader folds events from r (tests).
func DrainJSONLReader(r io.Reader, channelKey string) (*sessionSnapshot, error) {
	snap := &sessionSnapshot{
		Sessions:       make(map[string]*Session),
		UserSessions:   map[string][]string{channelKey: {}},
		ActiveSession:  make(map[string]string),
		Version:        snapshotVersion,
		PastIDTracking: true,
	}
	convOrder := make([]string, 0)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev SessionRecordEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		applySessionRecordEvent(snap, channelKey, &convOrder, &ev)
	}
	snap.UserSessions[channelKey] = uniqueStrings(convOrder)
	return snap, sc.Err()
}

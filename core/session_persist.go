package core

import "log/slog"

// SessionSnapshot is an exported view of SessionManager persistable state.
// Platform-specific backends (e.g. chat-api flat storage) use ExportSnapshot /
// ImportSnapshot together with SetSaveHook.
type SessionSnapshot struct {
	Sessions       map[string]*Session
	ActiveSession  map[string]string
	UserSessions   map[string][]string
	SessionNames   map[string]string
	UserMeta       map[string]*UserMeta
	Counter        int64
	LegacyData     bool
	PastIDTracking bool
	Version        int
}

// SessionSaveHook persists SessionManager state. It is invoked from saveLocked
// while sm.mu is held; the snapshot argument is safe to use without locking.
type SessionSaveHook func(sm *SessionManager, snap SessionSnapshot)

// SetSaveHook registers a custom persistence handler. When set, Save() invokes
// the hook instead of writing storePath. Intended for platform-specific stores.
func (sm *SessionManager) SetSaveHook(hook SessionSaveHook) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.saveHook = hook
}

// ExportSnapshot returns a deep copy of persistable state. Safe to call without
// holding sm.mu; it acquires a read lock internally.
func (sm *SessionManager) ExportSnapshot() SessionSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.exportSnapshotLocked()
}

func (sm *SessionManager) exportSnapshotLocked() SessionSnapshot {
	snapSessions := make(map[string]*Session, len(sm.sessions))
	for id, s := range sm.sessions {
		s.mu.Lock()
		agentSID := s.AgentSessionID
		if agentSID == ContinueSession {
			agentSID = ""
			s.AgentSessionID = ""
		}
		snapSessions[id] = &Session{
			ID:                  s.ID,
			Name:                s.Name,
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

	legacyData := sm.legacyData
	if legacyData {
		allTracked := true
		for _, s := range snapSessions {
			if s.AgentSessionID == "" && len(s.PastAgentSessionIDs) == 0 {
				allTracked = false
				break
			}
		}
		if allTracked {
			legacyData = false
			sm.legacyData = false
			slog.Info("session: legacy data migration complete, filtering re-enabled")
		}
	}

	return SessionSnapshot{
		Sessions:       snapSessions,
		ActiveSession:  cloneStringMap(sm.activeSession),
		UserSessions:   cloneStringSliceMap(sm.userSessions),
		SessionNames:   cloneStringMap(sm.sessionNames),
		UserMeta:       cloneUserMetaMap(sm.userMeta),
		Counter:        sm.counter,
		LegacyData:     legacyData,
		PastIDTracking: true,
		Version:        snapshotVersion,
	}
}

// ImportSnapshot replaces in-memory state from a snapshot produced by
// ExportSnapshot or a compatible external store.
func (sm *SessionManager) ImportSnapshot(snap SessionSnapshot) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessions = snap.Sessions
	sm.activeSession = snap.ActiveSession
	sm.userSessions = snap.UserSessions
	sm.sessionNames = snap.SessionNames
	sm.userMeta = snap.UserMeta
	sm.counter = snap.Counter
	if snap.Version >= snapshotVersion {
		sm.legacyData = snap.LegacyData
	} else {
		sm.legacyData = !snap.PastIDTracking
	}
	if sm.sessions == nil {
		sm.sessions = make(map[string]*Session)
	}
	if sm.activeSession == nil {
		sm.activeSession = make(map[string]string)
	}
	if sm.userSessions == nil {
		sm.userSessions = make(map[string][]string)
	}
	if sm.sessionNames == nil {
		sm.sessionNames = make(map[string]string)
	}
	if sm.userMeta == nil {
		sm.userMeta = make(map[string]*UserMeta)
	}
	for _, s := range sm.sessions {
		s.stripContinueSessionSentinel()
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func cloneUserMetaMap(in map[string]*UserMeta) map[string]*UserMeta {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*UserMeta, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		cp := *v
		out[k] = &cp
	}
	return out
}

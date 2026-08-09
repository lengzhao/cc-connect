package chatapi

import (
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// BindSessions wires the engine's session store into this platform instance.
// chat-api conversation metadata is sharded per user; the bound manager is kept
// for backward compatibility but HTTP handlers use SessionsForUser instead.
func (p *Platform) BindSessions(sm *core.SessionManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions = sm
}

// SessionsForUser implements core.UserShardedSessionStore.
func (p *Platform) SessionsForUser(userID string) *core.SessionManager {
	return p.sessionsForUser(userID)
}

// AttachSessions binds a session manager to every chat-api platform in the slice.
func AttachSessions(platforms []core.Platform, sm *core.SessionManager) {
	for _, plat := range platforms {
		if cp, ok := plat.(*Platform); ok {
			cp.BindSessions(sm)
		}
	}
}

func sessionStoreConfigFromOpts(opts map[string]any) (usersBase, legacyFile string) {
	if explicit := strings.TrimSpace(stringOption(opts, "session_store", "")); explicit != "" {
		if strings.HasSuffix(explicit, ".json") {
			return "", explicit
		}
		return explicit, ""
	}
	dataDir := stringOption(opts, "cc_data_dir", "")
	project := stringOption(opts, "cc_project", "")
	if dataDir == "" || project == "" {
		return "", ""
	}
	return sessionUsersBaseDir(dataDir, project), ""
}

// sessionUsersBaseDir returns sessions/{project}/users for per-user shard files.
func sessionUsersBaseDir(dataDir, project string) string {
	return filepath.Join(dataDir, "sessions", project, "users")
}

func (p *Platform) initSessionStore(opts map[string]any) {
	usersBase, legacyFile := sessionStoreConfigFromOpts(opts)
	p.sessionUsersBase = usersBase
	p.sessionStorePath = legacyFile
	if usersBase != "" {
		p.sessionShards = newUserShardCache(usersBase)
	}
}

// sessionsForUser returns the session manager for an end-user. Production
// deployments use flat storage under sessions/{project}/users/{user}/.
func (p *Platform) sessionsForUser(user string) *core.SessionManager {
	p.mu.Lock()
	shards := p.sessionShards
	legacyPath := p.sessionStorePath
	bound := p.sessions
	p.mu.Unlock()

	if shards != nil {
		return shards.forUser(user)
	}
	if bound != nil {
		return bound
	}
	if legacyPath == "" {
		return nil
	}
	return core.NewSessionManager(legacyPath)
}

func (p *Platform) sessionStoreReady() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionShards != nil || p.sessions != nil || p.sessionStorePath != ""
}

func (p *Platform) sessionsForConversation(conversationID string) *core.SessionManager {
	p.mu.Lock()
	shards := p.sessionShards
	legacyPath := p.sessionStorePath
	bound := p.sessions
	p.mu.Unlock()

	if shards != nil {
		return shards.findConversation(conversationID)
	}
	if bound != nil && bound.FindByID(conversationID) != nil {
		return bound
	}
	if legacyPath != "" {
		sm := core.NewSessionManager(legacyPath)
		if sm.FindByID(conversationID) != nil {
			return sm
		}
	}
	return nil
}

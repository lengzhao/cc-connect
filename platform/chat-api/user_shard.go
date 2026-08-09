package chatapi

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

// userShardCache lazily loads per-user SessionManagers backed by flat storage.
type userShardCache struct {
	mu      sync.Mutex
	baseDir string
	cache   map[string]*core.SessionManager
}

func newUserShardCache(baseDir string) *userShardCache {
	return &userShardCache{
		baseDir: baseDir,
		cache:   make(map[string]*core.SessionManager),
	}
}

func (c *userShardCache) forUser(userID string) *core.SessionManager {
	if c == nil || c.baseDir == "" {
		return core.NewSessionManager("")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return core.NewSessionManager("")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if sm, ok := c.cache[userID]; ok {
		return sm
	}
	userDir := filepath.Join(c.baseDir, safePathSegment(userID))
	sm := newSessionManagerForFlatUser(userDir, userID)
	c.cache[userID] = sm
	return sm
}

func (c *userShardCache) findConversation(conversationID string) *core.SessionManager {
	if c == nil || c.baseDir == "" || conversationID == "" {
		return nil
	}

	c.mu.Lock()
	for _, sm := range c.cache {
		if sm.FindByID(conversationID) != nil {
			c.mu.Unlock()
			return sm
		}
	}
	c.mu.Unlock()

	entries, err := os.ReadDir(c.baseDir)
	if err != nil {
		return nil
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		userDir := filepath.Join(c.baseDir, ent.Name())
		if !flatUserDirHasConversation(userDir, conversationID) {
			continue
		}
		userID := flatUserDirUserID(userDir)
		if userID == "" {
			userID = ent.Name()
		}
		return c.forUser(userID)
	}
	return nil
}

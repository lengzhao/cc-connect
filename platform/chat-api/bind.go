package chatapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// BindSessions wires the engine's session store into this platform instance.
// Production deployments must call this before Engine.Start (see AttachSessions).
func (p *Platform) BindSessions(sm *core.SessionManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions = sm
}

// AttachSessions binds a session manager to every chat-api platform in the slice.
func AttachSessions(platforms []core.Platform, sm *core.SessionManager) {
	for _, plat := range platforms {
		if cp, ok := plat.(*Platform); ok {
			cp.BindSessions(sm)
		}
	}
}

func sessionStorePathFromOpts(opts map[string]any) string {
	if explicit := stringOption(opts, "session_store", ""); explicit != "" {
		return explicit
	}
	dataDir := stringOption(opts, "cc_data_dir", "")
	project := stringOption(opts, "cc_project", "")
	if dataDir == "" || project == "" {
		return ""
	}
	workDir := stringOption(opts, "work_dir", "")
	return sessionStorePath(dataDir, project, workDir)
}

// sessionStorePath mirrors cmd/cc-connect sessionStorePath for the same persistence file.
func sessionStorePath(dataDir, name, workDir string) string {
	var filename string
	if workDir == "" {
		filename = name + ".json"
	} else {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			abs = workDir
		}
		h := sha256.Sum256([]byte(abs))
		short := hex.EncodeToString(h[:4])
		filename = fmt.Sprintf("%s_%s.json", name, short)
	}

	for _, legacy := range []string{
		filepath.Join(dataDir, filename),
		filepath.Join(dataDir, strings.TrimSuffix(filename, ".json")+".sessions.json"),
	} {
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	return filepath.Join(dataDir, "sessions", filename)
}

// sessionsOrReload returns the bound session manager, or reloads from disk when
// only sessionStorePath is configured (read-mostly fallback).
func (p *Platform) sessionsOrReload() *core.SessionManager {
	p.mu.Lock()
	sm := p.sessions
	path := p.sessionStorePath
	p.mu.Unlock()
	if sm != nil {
		return sm
	}
	if path == "" {
		return nil
	}
	return core.NewSessionManager(path)
}

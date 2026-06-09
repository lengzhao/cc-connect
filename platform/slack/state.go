package slack

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	slackStateVersion        = 1
	defaultThreadActiveTTL   = 72 * time.Hour
	defaultInboundDedupTTL   = 60 * time.Second
	defaultThreadActiveHours = 72
	defaultInboundDedupSecs  = 60
)

type slackThreadStateSnapshot struct {
	Version       int                  `json:"version"`
	ActiveThreads map[string]time.Time `json:"active_threads"`
}

func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '\x00':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func resolveSlackStatePath(opts map[string]any) string {
	if override, _ := opts["state_dir"].(string); strings.TrimSpace(override) != "" {
		return filepath.Join(strings.TrimSpace(override), "state.json")
	}
	dataDir, _ := opts["cc_data_dir"].(string)
	project, _ := opts["cc_project"].(string)
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(project) == "" {
		return ""
	}
	return filepath.Join(dataDir, "slack", sanitizePathSegment(project), "state.json")
}

func parsePositiveIntOpt(v any, fallback int) int {
	switch n := v.(type) {
	case int:
		if n > 0 {
			return n
		}
	case int64:
		if n > 0 {
			return int(n)
		}
	case float64:
		if n > 0 {
			return int(n)
		}
	}
	return fallback
}

func (p *Platform) loadSlackThreadState() {
	if p.statePath == "" {
		return
	}
	data, err := os.ReadFile(p.statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("slack: failed to read thread state", "path", p.statePath, "error", err)
		}
		return
	}
	var snap slackThreadStateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		slog.Warn("slack: failed to parse thread state", "path", p.statePath, "error", err)
		return
	}
	if snap.ActiveThreads == nil {
		return
	}

	p.threadStateMu.Lock()
	defer p.threadStateMu.Unlock()
	if p.activeThreads == nil {
		p.activeThreads = make(map[string]time.Time)
	}
	now := time.Now()
	for key, markedAt := range snap.ActiveThreads {
		if now.Sub(markedAt) <= p.threadActiveTTL {
			p.activeThreads[key] = markedAt
		}
	}
	slog.Info("slack: loaded thread state", "path", p.statePath, "active_threads", len(p.activeThreads))
}

func (p *Platform) saveSlackThreadState() {
	if p.statePath == "" {
		return
	}
	p.threadStateMu.Lock()
	defer p.threadStateMu.Unlock()
	p.pruneExpiredThreadsLocked(time.Now())

	snap := slackThreadStateSnapshot{
		Version:       slackStateVersion,
		ActiveThreads: make(map[string]time.Time, len(p.activeThreads)),
	}
	for key, markedAt := range p.activeThreads {
		snap.ActiveThreads[key] = markedAt
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		slog.Error("slack: failed to marshal thread state", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p.statePath), 0o755); err != nil {
		slog.Error("slack: failed to create thread state dir", "path", p.statePath, "error", err)
		return
	}
	if err := core.AtomicWriteFile(p.statePath, data, 0o644); err != nil {
		slog.Error("slack: failed to write thread state", "path", p.statePath, "error", err)
	}
}

func (p *Platform) pruneExpiredThreadsLocked(now time.Time) {
	for key, markedAt := range p.activeThreads {
		if now.Sub(markedAt) > p.threadActiveTTL {
			delete(p.activeThreads, key)
		}
	}
}

func (p *Platform) pruneExpiredInboundLocked(now time.Time) {
	for key, seenAt := range p.recentInbound {
		if now.Sub(seenAt) > p.dedupTTL {
			delete(p.recentInbound, key)
		}
	}
}

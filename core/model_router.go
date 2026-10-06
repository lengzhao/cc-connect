package core

import (
	"context"
	"log/slog"
	"strings"
)

// ModelRouteRequest contains only the current user turn and its conversation identity.
type ModelRouteRequest struct{ SessionKey, SessionID, Content, WorkDir string }

// SessionModelStarter starts a conversation with a model override without mutating
// the shared Agent or provider. Implementations must preserve resume semantics.
type SessionModelStarter interface {
	StartSessionWithModel(context.Context, string, string) (AgentSession, error)
}

// SetModelRouter installs an optional per-conversation policy before Engine.Start.
// Explicit workspace /model selections take precedence. perTurn=false caches a
// successful decision on the persisted Session, including a default-model choice.
func (e *Engine) SetModelRouter(router func(context.Context, ModelRouteRequest) (string, error), perTurn bool) {
	e.modelRouter = router
	e.modelRoutePerTurn = perTurn
}

func (e *Engine) routeTurnModel(msg *Message, session *Session, sessions *SessionManager, agent Agent, interactiveKey string) string {
	if _, pinned := e.manualModelAgents.Load(agent); pinned {
		return ""
	}
	if e.projectState != nil && !e.multiWorkspace && e.projectState.WorkspaceModelOverride("__project_model_pin__") != "" {
		return ""
	}
	if e.modelRouter == nil {
		return ""
	}
	if _, ok := agent.(SessionModelStarter); !ok {
		return ""
	}
	if e.projectState != nil && e.projectState.WorkspaceModelOverride(workspaceModelOverrideKey(interactiveKey, msg.SessionKey, agent)) != "" {
		return ""
	}
	session.mu.Lock()
	cached := session.AutomaticModel
	session.mu.Unlock()
	if !e.modelRoutePerTurn && cached != nil {
		return *cached
	}
	workDir := ""
	if a, ok := agent.(interface{ GetWorkDir() string }); ok {
		workDir = a.GetWorkDir()
	}
	model, err := e.modelRouter(e.ctx, ModelRouteRequest{SessionKey: msg.SessionKey, SessionID: session.ID, Content: msg.Content, WorkDir: workDir})
	if err != nil {
		slog.Warn("automatic model routing failed; using default model")
		return ""
	}
	model = strings.TrimSpace(model)
	session.mu.Lock()
	session.AutomaticModel = &model
	session.mu.Unlock()
	sessions.Save()
	return model
}

func startRoutedSession(ctx context.Context, agent Agent, id, model string) (AgentSession, error) {
	if model != "" {
		if starter, ok := agent.(SessionModelStarter); ok {
			return starter.StartSessionWithModel(ctx, id, model)
		}
	}
	return agent.StartSession(ctx, id)
}

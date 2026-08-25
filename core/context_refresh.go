package core

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

const maxContextUpdateNoticeItems = 50
const contextCheckpointMarker = "__cc_connect_context_checkpoint__"

type preparedContextRefresh struct {
	notice   string
	versions map[string]string
}

func prepareContextRefresh(agent Agent, session *Session) preparedContextRefresh {
	provider, ok := agent.(ContextResourceProvider)
	if !ok || session == nil {
		return preparedContextRefresh{}
	}
	resources, err := provider.ContextResources()
	if err != nil {
		slog.Warn("context refresh: scan resources failed", "error", err)
		return preparedContextRefresh{}
	}
	current := make(map[string]string, len(resources)+1)
	current[contextCheckpointMarker] = "1"
	byKey := make(map[string]ContextResource, len(resources))
	for _, resource := range resources {
		kind := strings.TrimSpace(resource.Kind)
		path := strings.TrimSpace(resource.Path)
		version := strings.TrimSpace(resource.Version)
		if kind == "" || path == "" || version == "" {
			continue
		}
		key := kind + ":" + path
		current[key] = version
		byKey[key] = resource
	}

	previous := session.GetContextResourceVersions()
	if previous == nil {
		// A new or legacy conversation starts at the current effective state.
		// Its agent process already loaded current Automon instructions.
		return preparedContextRefresh{versions: current}
	}

	type change struct {
		key      string
		resource ContextResource
		deleted  bool
	}
	changes := make([]change, 0)
	for key, resource := range byKey {
		if previous[key] != current[key] {
			changes = append(changes, change{key: key, resource: resource})
		}
	}
	for key := range previous {
		if key == contextCheckpointMarker {
			continue
		}
		if _, exists := current[key]; exists {
			continue
		}
		kind, path, _ := strings.Cut(key, ":")
		changes = append(changes, change{key: key, resource: ContextResource{Kind: kind, Path: path}, deleted: true})
	}
	if len(changes) == 0 {
		return preparedContextRefresh{}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].key < changes[j].key })

	var b strings.Builder
	b.WriteString("<automon_context_updates>\n")
	b.WriteString("The effective Automon context changed after this conversation's previous accepted turn:\n")
	limit := len(changes)
	if limit > maxContextUpdateNoticeItems {
		limit = maxContextUpdateNoticeItems
	}
	for _, item := range changes[:limit] {
		status := "updated"
		if _, existed := previous[item.key]; !existed {
			status = "added"
		}
		if item.deleted {
			status = "deleted"
		}
		fmt.Fprintf(&b, "- %s %s: %s", item.resource.Kind, status, item.resource.Path)
		if !item.resource.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, " (%s)", item.resource.UpdatedAt.UTC().Format(time.RFC3339))
		}
		b.WriteByte('\n')
	}
	if len(changes) > limit {
		fmt.Fprintf(&b, "- and %d more context files\n", len(changes)-limit)
	}
	b.WriteString("Before answering, re-read every updated Automon/runtime-instruction file because it is authoritative. ")
	b.WriteString("Read updated Skills, Memory, or Knowledge only when relevant to the user's current request. ")
	b.WriteString("Do not discuss this refresh notice unless it materially affects the answer.\n")
	b.WriteString("</automon_context_updates>")
	return preparedContextRefresh{notice: b.String(), versions: current}
}

func appendContextRefreshNotice(prompt, notice string) string {
	if strings.TrimSpace(notice) == "" {
		return prompt
	}
	return notice + "\n\n<current_user_request>\n" + prompt + "\n</current_user_request>"
}

func (e *Engine) contextRefreshAgent(state *interactiveState) Agent {
	if state != nil {
		state.mu.Lock()
		agent := state.agent
		state.mu.Unlock()
		if agent != nil {
			return agent
		}
	}
	return e.agent
}

func (e *Engine) sendWithContextRefresh(agent Agent, agentSession AgentSession, session *Session, sessions *SessionManager, prompt string, images []ImageAttachment, files []FileAttachment) error {
	refresh := prepareContextRefresh(agent, session)
	prompt = appendContextRefreshNotice(prompt, refresh.notice)
	if err := agentSession.Send(prompt, images, files); err != nil {
		return err
	}
	if refresh.versions != nil {
		session.SetContextResourceVersions(refresh.versions)
		sessions.Save()
	}
	return nil
}

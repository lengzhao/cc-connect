package chatapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

const (
	autoNameMaxRunes  = 32
	nameRunPrefix     = "name_run_"
	nameMinInputRunes = 8
)

func (p *Platform) shouldGenerateAIName(query string) bool {
	return p.autoGenerateNameMode == autoGenerateNameModeAI &&
		utf8.RuneCountInString(query) >= nameMinInputRunes
}

func (p *Platform) handleGenerateConversationName(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	user, ok := p.resolveUser(w, r, true)
	if !ok {
		return
	}
	sessions := p.sessionsOrReload()
	if sessions == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	session := p.findOwnedConversation(sessions, user, conversationID)
	if session == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	var body struct {
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !body.Force && session.GetName() != "" && session.GetName() != "default" {
		writeOK(w, http.StatusOK, map[string]string{"status": "skipped", "name": session.GetName()})
		return
	}
	runID := newNameRunID()
	p.startManualNameGeneration(runID, session, sessions, body.Force)
	writeOK(w, http.StatusAccepted, map[string]string{"name_run_id": runID, "status": "running"})
}

func newNameRunID() string {
	return nameRunPrefix + newRunID()[len("run_"):]
}

func (p *Platform) startAutoNameGeneration(runID string, session *core.Session, sessions *core.SessionManager, query string) {
	if p.shouldGenerateAIName(query) && p.startProviderNameGeneration(runID, session, sessions, false, query) {
		return
	}
	p.startHeuristicNameGeneration(session, sessions, false, query)
}

func (p *Platform) startManualNameGeneration(runID string, session *core.Session, sessions *core.SessionManager, force bool) {
	if p.startProviderNameGeneration(runID, session, sessions, force, "") {
		return
	}
	p.startHeuristicNameGeneration(session, sessions, force, "")
}

func (p *Platform) startProviderNameGeneration(runID string, session *core.Session, sessions *core.SessionManager, force bool, query string) bool {
	if p.nameModel != "" && p.nameProviderAPIKey != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), defaultNameRequestTimeout)
			defer cancel()
			name, err := p.generateNameWithProvider(ctx, buildNamePrompt(session.GetHistory(0), query))
			if err != nil {
				slog.Warn("chat-api: name generation failed", "run_id", runID, "error", err)
				applyGeneratedName(session, sessions, heuristicNameSeed(session, query), force)
				return
			}
			if strings.TrimSpace(name) == "" {
				applyGeneratedName(session, sessions, heuristicNameSeed(session, query), force)
				return
			}
			applyGeneratedName(session, sessions, name, force)
		}()
		return true
	}
	return false
}

func (p *Platform) startHeuristicNameGeneration(session *core.Session, sessions *core.SessionManager, force bool, query string) {
	go func() {
		applyGeneratedName(session, sessions, heuristicNameSeed(session, query), force)
	}()
}

func heuristicNameSeed(session *core.Session, query string) string {
	if strings.TrimSpace(query) != "" {
		return autoNameFromQuery(query)
	}
	for _, entry := range session.GetHistory(0) {
		if entry.Role == "user" && strings.TrimSpace(entry.Content) != "" {
			return autoNameFromQuery(entry.Content)
		}
	}
	return ""
}

func applyGeneratedName(session *core.Session, sessions *core.SessionManager, name string, force bool) {
	if strings.TrimSpace(name) == "" {
		return
	}
	if !force && !session.SetNameIfDefault(name) {
		return
	}
	if force {
		session.SetName(name)
	}
	sessions.Save()
}

func (p *Platform) generateNameWithProvider(ctx context.Context, prompt string) (string, error) {
	switch p.nameProviderType {
	case "openai", "openai-compatible":
		return p.generateOpenAIName(ctx, prompt)
	case "claude":
		return p.generateClaudeName(ctx, prompt)
	default:
		return "", fmt.Errorf("unsupported name provider type %q", p.nameProviderType)
	}
}

func (p *Platform) generateOpenAIName(ctx context.Context, prompt string) (string, error) {
	baseURL := strings.TrimRight(p.nameProviderBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if !strings.HasSuffix(baseURL, "/chat/completions") {
		baseURL += "/chat/completions"
	}
	body, err := json.Marshal(map[string]any{
		"model": p.nameModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature":           0.2,
		"max_completion_tokens": 64,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.nameProviderAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode provider response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("provider returned no choices")
	}
	return sanitizeGeneratedName(result.Choices[0].Message.Content), nil
}

func (p *Platform) generateClaudeName(ctx context.Context, prompt string) (string, error) {
	baseURL := strings.TrimRight(p.nameProviderBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/messages"
	} else if !strings.HasSuffix(baseURL, "/v1/messages") {
		baseURL += "/v1/messages"
	}
	body, err := json.Marshal(map[string]any{
		"model":      p.nameModel,
		"max_tokens": 64,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-api-key", p.nameProviderAPIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request provider: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode provider response: %w", err)
	}
	for _, block := range result.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return sanitizeGeneratedName(block.Text), nil
		}
	}
	return "", fmt.Errorf("provider returned no text content")
}

func buildNamePrompt(history []core.HistoryEntry, query string) string {
	var b strings.Builder
	b.WriteString("Generate a short conversation name based on the dialogue below. Use the same language as the dialogue. Output only the name, without quotes, explanations, or surrounding punctuation. Maximum 32 characters.")
	b.WriteString("\n\n")
	for _, entry := range history {
		if entry.Role != "user" && entry.Role != "assistant" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", entry.Role, entry.Content)
	}
	if strings.TrimSpace(query) != "" {
		fmt.Fprintf(&b, "user: %s\n", query)
	}
	return b.String()
}

func sanitizeGeneratedName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, `\n`, " ")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.Trim(name, "`\"'")
	name = strings.TrimSpace(name)
	if strings.HasSuffix(name, "\"") || strings.HasSuffix(name, "'") || strings.HasSuffix(name, "`") {
		name = name[:len(name)-1]
	}
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "名称：")
	name = strings.TrimPrefix(name, "名称:")
	return truncateRunes(name, autoNameMaxRunes)
}

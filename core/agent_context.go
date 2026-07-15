package core

import (
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	maxAgentContextValueLen   = 128
	maxAgentContextCustomKeys = 16
)

var (
	agentContextLanguageRe  = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)
	agentContextCustomKeyRe = regexp.MustCompile(`^custom\.[a-z][a-z0-9_]{0,31}$`)
	agentContextIDRe        = regexp.MustCompile(`^[A-Za-z0-9._:@/-]{1,128}$`)
)

// AgentContext carries per-turn contextual hints for the agent prompt.
// It is never persisted on Session and never written into History.
type AgentContext struct {
	Language string            // BCP47-like language hint for the agent (not Engine UI i18n)
	TaskID   string            // caller task / business id
	TraceID  string            // distributed trace id
	Custom   map[string]string // only keys matching custom.<slug>
}

// Empty reports whether the context has no fields to inject.
func (c AgentContext) Empty() bool {
	return c.Language == "" && c.TaskID == "" && c.TraceID == "" && len(c.Custom) == 0
}

// Clone returns a deep copy suitable for storing on queued messages.
func (c AgentContext) Clone() AgentContext {
	out := AgentContext{
		Language: c.Language,
		TaskID:   c.TaskID,
		TraceID:  c.TraceID,
	}
	if len(c.Custom) > 0 {
		out.Custom = make(map[string]string, len(c.Custom))
		for k, v := range c.Custom {
			out.Custom[k] = v
		}
	}
	return out
}

// SanitizeAgentContext validates and cleans platform-provided context.
// Invalid keys/values are dropped (with a warn log that never includes values).
func SanitizeAgentContext(in AgentContext) AgentContext {
	out := AgentContext{}
	if lang := strings.TrimSpace(in.Language); lang != "" {
		cleaned := sanitizeAgentContextValue(lang)
		if agentContextLanguageRe.MatchString(cleaned) {
			out.Language = cleaned
		} else {
			slog.Warn("agent context: dropping invalid language", "reason", "format")
		}
	}
	if task := strings.TrimSpace(in.TaskID); task != "" {
		cleaned := sanitizeAgentContextID(task)
		if cleaned != "" && agentContextIDRe.MatchString(cleaned) {
			out.TaskID = cleaned
		} else {
			slog.Warn("agent context: dropping invalid task_id", "reason", "format")
		}
	}
	if trace := strings.TrimSpace(in.TraceID); trace != "" {
		cleaned := sanitizeAgentContextID(trace)
		if cleaned != "" && agentContextIDRe.MatchString(cleaned) {
			out.TraceID = cleaned
		} else {
			slog.Warn("agent context: dropping invalid trace_id", "reason", "format")
		}
	}

	if len(in.Custom) == 0 {
		return out
	}
	keys := make([]string, 0, len(in.Custom))
	for k := range in.Custom {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out.Custom = make(map[string]string)
	for _, k := range keys {
		if len(out.Custom) >= maxAgentContextCustomKeys {
			slog.Warn("agent context: dropping custom key", "key", k, "reason", "too_many")
			continue
		}
		if !agentContextCustomKeyRe.MatchString(k) {
			slog.Warn("agent context: dropping custom key", "key", k, "reason", "invalid_key")
			continue
		}
		raw := strings.TrimSpace(in.Custom[k])
		if raw == "" {
			continue
		}
		cleaned := sanitizeAgentContextValue(raw)
		if cleaned == "" {
			continue
		}
		out.Custom[k] = cleaned
	}
	if len(out.Custom) == 0 {
		out.Custom = nil
	}
	return out
}

// FilterAgentContextByAllowlist keeps only fields listed in allowlist.
// Supported entries: language, task_id, trace_id, custom.<slug>, custom.*
// An empty/nil allowlist drops everything.
func FilterAgentContextByAllowlist(in AgentContext, allowlist []string) AgentContext {
	if len(allowlist) == 0 || in.Empty() {
		return AgentContext{}
	}
	allow := make(map[string]struct{}, len(allowlist))
	allowAllCustom := false
	for _, raw := range allowlist {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		if key == "custom.*" {
			allowAllCustom = true
			continue
		}
		allow[key] = struct{}{}
	}
	out := AgentContext{}
	if _, ok := allow["language"]; ok {
		out.Language = in.Language
	}
	if _, ok := allow["task_id"]; ok {
		out.TaskID = in.TaskID
	}
	if _, ok := allow["trace_id"]; ok {
		out.TraceID = in.TraceID
	}
	if len(in.Custom) == 0 {
		return out
	}
	out.Custom = make(map[string]string)
	for k, v := range in.Custom {
		if allowAllCustom {
			out.Custom[k] = v
			continue
		}
		if _, ok := allow[k]; ok {
			out.Custom[k] = v
		}
	}
	if len(out.Custom) == 0 {
		out.Custom = nil
	}
	return out
}

// NormalizeInjectContextAllowlist canonicalizes project-level inject_context entries.
func NormalizeInjectContextAllowlist(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		var key string
		switch {
		case lower == "language" || lower == "task_id" || lower == "trace_id" || lower == "custom.*":
			key = lower
		case agentContextCustomKeyRe.MatchString(trimmed):
			key = trimmed
		default:
			slog.Warn("inject_context: ignoring unknown entry", "key", trimmed)
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

// PromptAttrs returns stable-ordered attribute strings for the [cc-connect ...] header.
func (c AgentContext) PromptAttrs() []string {
	if c.Empty() {
		return nil
	}
	var attrs []string
	if c.Language != "" {
		attrs = append(attrs, `language="`+promptAttrValue(c.Language)+`"`)
	}
	if c.TaskID != "" {
		attrs = append(attrs, `task_id="`+promptAttrValue(c.TaskID)+`"`)
	}
	if c.TraceID != "" {
		attrs = append(attrs, `trace_id="`+promptAttrValue(c.TraceID)+`"`)
	}
	if len(c.Custom) > 0 {
		keys := make([]string, 0, len(c.Custom))
		for k := range c.Custom {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			attrs = append(attrs, k+`="`+promptAttrValue(c.Custom[k])+`"`)
		}
	}
	return attrs
}

func sanitizeAgentContextValue(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	v = strings.TrimSpace(v)
	if len(v) > maxAgentContextValueLen {
		v = v[:maxAgentContextValueLen]
	}
	return v
}

func sanitizeAgentContextID(v string) string {
	v = sanitizeAgentContextValue(v)
	v = strings.ReplaceAll(v, `"`, "")
	v = strings.ReplaceAll(v, `'`, "")
	v = strings.TrimSpace(v)
	if len(v) > maxAgentContextValueLen {
		v = v[:maxAgentContextValueLen]
	}
	return v
}

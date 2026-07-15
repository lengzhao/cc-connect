package chatapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// agentContextHeaderMap maps AgentContext field names to HTTP header names.
// Keys: language | task_id | trace_id | custom.<slug>
type agentContextHeaderMap map[string]string

func agentContextHeadersOption(opts map[string]any) (agentContextHeaderMap, error) {
	if opts == nil {
		return nil, nil
	}
	raw, ok := opts["agent_context_headers"]
	if !ok || raw == nil {
		return nil, nil
	}
	src := map[string]any{}
	switch v := raw.(type) {
	case map[string]any:
		src = v
	case map[string]string:
		for k, val := range v {
			src[k] = val
		}
	default:
		return nil, fmt.Errorf("chat-api: agent_context_headers must be a table/map")
	}
	out := make(agentContextHeaderMap, len(src))
	seenHeaders := make(map[string]string, len(src))
	for k, v := range src {
		field := strings.TrimSpace(k)
		headerName := strings.TrimSpace(fmt.Sprint(v))
		if field == "" || headerName == "" {
			continue
		}
		canonField, err := normalizeAgentContextField(field)
		if err != nil {
			return nil, fmt.Errorf("chat-api: agent_context_headers: %w", err)
		}
		canonHeader := http.CanonicalHeaderKey(headerName)
		if isBlockedAgentContextHeader(canonHeader) {
			return nil, fmt.Errorf("chat-api: agent_context_headers: header %q is blocked", canonHeader)
		}
		lowerHeader := strings.ToLower(canonHeader)
		if prev, ok := seenHeaders[lowerHeader]; ok {
			return nil, fmt.Errorf("chat-api: agent_context_headers: header %q mapped twice (%s and %s)", canonHeader, prev, canonField)
		}
		if _, ok := out[canonField]; ok {
			return nil, fmt.Errorf("chat-api: agent_context_headers: field %q mapped twice", canonField)
		}
		seenHeaders[lowerHeader] = canonField
		out[canonField] = canonHeader
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func normalizeAgentContextField(field string) (string, error) {
	trimmed := strings.TrimSpace(field)
	if trimmed == "" {
		return "", fmt.Errorf("empty field")
	}
	lower := strings.ToLower(trimmed)
	switch lower {
	case "language", "task_id", "trace_id":
		return lower, nil
	}
	if strings.HasPrefix(trimmed, "custom.") {
		if trimmed != lower {
			return "", fmt.Errorf("field %q must be lowercase", field)
		}
		got := core.NormalizeInjectContextAllowlist([]string{trimmed})
		if len(got) != 1 || got[0] != trimmed {
			return "", fmt.Errorf("invalid field %q", field)
		}
		return trimmed, nil
	}
	return "", fmt.Errorf("unsupported field %q", field)
}

func isBlockedAgentContextHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "cookie", "proxy-authorization", "set-cookie",
		"www-authenticate", "proxy-authenticate", "host", "content-length",
		"transfer-encoding", "connection":
		return true
	default:
		return false
	}
}

func (m agentContextHeaderMap) headerNames() []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	seen := make(map[string]struct{}, len(m))
	for _, h := range m {
		key := strings.ToLower(h)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// collectAgentContext reads configured headers into AgentContext.
// Values are copied as plain strings; callers must not share the request header map.
func (m agentContextHeaderMap) collectAgentContext(r *http.Request) core.AgentContext {
	if len(m) == 0 || r == nil {
		return core.AgentContext{}
	}
	out := core.AgentContext{}
	for field, headerName := range m {
		value := strings.TrimSpace(r.Header.Get(headerName))
		if value == "" {
			continue
		}
		// Copy value defensively (Header.Get already returns a string copy).
		switch field {
		case "language":
			out.Language = value
		case "task_id":
			out.TaskID = value
		case "trace_id":
			out.TraceID = value
		default:
			if strings.HasPrefix(field, "custom.") {
				if out.Custom == nil {
					out.Custom = make(map[string]string)
				}
				out.Custom[field] = value
			} else {
				slog.Warn("chat-api: ignoring unmapped agent context field", "field", field)
			}
		}
	}
	return core.SanitizeAgentContext(out)
}

package a2a

import (
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

var blockedForwardHeaders = map[string]struct{}{
	"authorization":       {},
	"cookie":              {},
	"proxy-authorization": {},
	"set-cookie":          {},
	"www-authenticate":    {},
	"proxy-authenticate":  {},
}

func normalizeForwardHeaderNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		canon := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canon == "" || isBlockedForwardHeader(canon) {
			continue
		}
		key := strings.ToLower(canon)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, canon)
	}
	return out
}

func isBlockedForwardHeader(name string) bool {
	_, blocked := blockedForwardHeaders[strings.ToLower(strings.TrimSpace(name))]
	return blocked
}

func collectForwardedHeaders(configured []string, params *a2asrv.ServiceParams) map[string]string {
	if len(configured) == 0 || params == nil {
		return nil
	}
	out := make(map[string]string)
	for _, name := range configured {
		if isBlockedForwardHeader(name) {
			continue
		}
		values, ok := params.Get(name)
		if !ok || len(values) == 0 {
			continue
		}
		trimmed := make([]string, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				trimmed = append(trimmed, value)
			}
		}
		if len(trimmed) == 0 {
			continue
		}
		out[name] = strings.Join(trimmed, ", ")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectHookContext(requestMetadata, messageMetadata map[string]any) map[string]any {
	out := make(map[string]any, len(requestMetadata)+len(messageMetadata))
	for k, v := range requestMetadata {
		if strings.TrimSpace(k) != "" {
			out[k] = v
		}
	}
	for k, v := range messageMetadata {
		if strings.TrimSpace(k) != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

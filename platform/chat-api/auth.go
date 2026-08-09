package chatapi

import (
	"net/http"
	"strings"
)

const (
	defaultUserHeader     = "X-Chat-API-User"
	defaultUserNameHeader = "X-Chat-API-User-Name"
	defaultChannelHeader  = "X-Chat-API-Channel"
	maxUserLen            = 128
	maxUserNameLen        = 128
	maxChannelLen         = 256
)

func (p *Platform) authHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p.apiToken != "" {
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			token, ok := strings.CutPrefix(auth, "Bearer ")
			if !ok || strings.TrimSpace(token) != p.apiToken {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next(w, r)
	}
}

func (p *Platform) responseHeaderHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.setResponseHeader(w)
		next(w, r)
	}
}

func (p *Platform) corsHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.setCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func (p *Platform) setCORS(w http.ResponseWriter, r *http.Request) {
	if len(p.corsOrigins) == 0 {
		return
	}
	origin := r.Header.Get("Origin")
	for _, allowed := range p.corsOrigins {
		if allowed == "*" || origin == allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			allowHeaders := []string{
				"Authorization", "Content-Type", "Accept",
				p.userHeader, p.userNameHeader, p.channelHeader,
			}
			allowHeaders = append(allowHeaders, p.agentContextHeaders.headerNames()...)
			allowHeaders = append(allowHeaders, p.forwardHeaders...)
			if p.responseHeader.enabled() {
				allowHeaders = append(allowHeaders, p.responseHeader.name)
			}
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(allowHeaders, ", "))
			if p.responseHeader.enabled() {
				w.Header().Set("Access-Control-Expose-Headers", p.responseHeader.name)
			}
			return
		}
	}
}

// resolveUser returns the end-user id. Writes return false when invalid.
func (p *Platform) resolveUser(w http.ResponseWriter, r *http.Request, writeOnly bool) (string, bool) {
	if writeOnly {
		user := strings.TrimSpace(r.Header.Get(p.userHeader))
		if user == "" {
			writeErr(w, http.StatusBadRequest, "user required")
			return "", false
		}
		if !validUser(user) {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return "", false
		}
		return user, true
	}

	user := strings.TrimSpace(r.URL.Query().Get("user"))
	if user == "" {
		user = strings.TrimSpace(r.Header.Get(p.userHeader))
	}
	if user == "" {
		writeErr(w, http.StatusBadRequest, "user required")
		return "", false
	}
	if !validUser(user) {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return "", false
	}
	return user, true
}

// resolveUserName returns the optional display name from user_name_header.
// Empty means the caller should fall back to the user id.
func (p *Platform) resolveUserName(r *http.Request) string {
	name := strings.TrimSpace(r.Header.Get(p.userNameHeader))
	if name == "" || len(name) > maxUserNameLen {
		return ""
	}
	return name
}

func displayUserName(userID, userName string) string {
	if userName != "" {
		return userName
	}
	return userID
}

// resolveChannel returns an optional workspace channel id from channel_header.
// Empty header is allowed (falls back to user id in channelKeyForMessage).
// Invalid values write 400 and return ok=false.
func (p *Platform) resolveChannel(w http.ResponseWriter, r *http.Request) (string, bool) {
	channel := strings.TrimSpace(r.Header.Get(p.channelHeader))
	if channel == "" {
		return "", true
	}
	if !validChannel(channel) || !isSafeWorkspaceChannelPath(channel) {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return "", false
	}
	return channel, true
}

func validUser(user string) bool {
	if user == "" || len(user) > maxUserLen {
		return false
	}
	for _, r := range user {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == ':', r == '.':
		default:
			return false
		}
	}
	return true
}

func validChannel(channel string) bool {
	if channel == "" || len(channel) > maxChannelLen {
		return false
	}
	for _, r := range channel {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == ':', r == '.', r == '/':
		default:
			return false
		}
	}
	return true
}

func sessionKeyForUser(user string) string {
	return "chat-api:" + user
}

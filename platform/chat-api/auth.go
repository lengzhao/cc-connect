package chatapi

import (
	"net/http"
	"strings"
)

const (
	defaultUserHeader     = "X-Chat-API-User"
	defaultUserNameHeader = "X-Chat-API-User-Name"
	maxUserLen            = 128
	maxUserNameLen        = 128
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
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, "+p.userHeader+", "+p.userNameHeader)
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

func sessionKeyForUser(user string) string {
	return "chat-api:" + user
}

// sessionKeyForConversation routes Engine state per shared conversation (Slack channel scope).
func sessionKeyForConversation(conversationID string) string {
	return "chat-api:conv:" + conversationID
}

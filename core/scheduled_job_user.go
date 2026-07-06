package core

import (
	"log/slog"
	"strings"
)

const (
	cronSyntheticUserID  = "cron"
	timerSyntheticUserID = "timer"
)

type scheduledUserPolicy struct {
	component           string
	syntheticUserID     string
	rejectCron          bool
	warnOnSyntheticCron bool
}

var (
	cronUserPolicy = scheduledUserPolicy{
		component:           "cron",
		syntheticUserID:     cronSyntheticUserID,
		rejectCron:          false,
		warnOnSyntheticCron: true,
	}
	timerUserPolicy = scheduledUserPolicy{
		component:           "timer",
		syntheticUserID:     timerSyntheticUserID,
		rejectCron:          true,
		warnOnSyntheticCron: false,
	}
)

func userIdentityFromMessage(msg *Message) (userID, userName, userEmail string) {
	if msg == nil {
		return "", "", ""
	}
	return strings.TrimSpace(msg.UserID), strings.TrimSpace(msg.UserName), strings.TrimSpace(msg.UserEmail)
}

func ensureJobUserID(sessionKey string, userID *string) {
	if strings.TrimSpace(*userID) == "" {
		*userID = userIDFromSessionKey(sessionKey)
	}
}

func isCronSyntheticUser(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "cron")
}

func resolveScheduledJobUser(p scheduledUserPolicy, jobID, sessionKey, storedID, storedName, storedEmail string) (userID, userName, userEmail string) {
	userID = strings.TrimSpace(storedID)
	userName = strings.TrimSpace(storedName)
	userEmail = strings.TrimSpace(storedEmail)

	usedFallback := false
	userID = resolveScheduledJobUserID(p, jobID, sessionKey, userID, &usedFallback)

	if userName == "" {
		if userID == p.syntheticUserID {
			userName = p.syntheticUserID
		} else {
			userName = userID
		}
	}
	if p.rejectCron && isCronSyntheticUser(userName) {
		slog.Warn(p.component+": rejected user_name \"cron\"",
			"job_id", jobID, "session_key", sessionKey)
		if userID != p.syntheticUserID {
			userName = userID
		} else {
			userName = p.syntheticUserID
		}
	}
	if p.warnOnSyntheticCron && (isCronSyntheticUser(userID) || isCronSyntheticUser(userName)) {
		slog.Warn(p.component+": using synthetic user identity \"cron\"",
			"job_id", jobID, "session_key", sessionKey,
			"user_id", userID, "user_name", userName, "fallback", usedFallback)
	}
	return userID, userName, userEmail
}

func resolveScheduledJobUserID(p scheduledUserPolicy, jobID, sessionKey, userID string, usedFallback *bool) string {
	if p.rejectCron && isCronSyntheticUser(userID) {
		slog.Warn(p.component+": rejected user_id \"cron\", resolving from session_key",
			"job_id", jobID, "session_key", sessionKey)
		userID = ""
	}
	if userID == "" {
		userID = userIDFromSessionKey(sessionKey)
	}
	if p.rejectCron && isCronSyntheticUser(userID) {
		slog.Warn(p.component+": session_key resolved to \"cron\", rejecting",
			"job_id", jobID, "session_key", sessionKey)
		userID = ""
	}
	if userID == "" {
		userID = p.syntheticUserID
		*usedFallback = true
		if !p.warnOnSyntheticCron {
			slog.Warn(p.component+": unable to resolve user identity, using synthetic fallback",
				"job_id", jobID, "session_key", sessionKey, "fallback", p.syntheticUserID)
		}
	}
	return userID
}

func userIDFromSessionKey(sessionKey string) string {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return ""
	}
	// Drop optional workspace prefix: "/path/to/project:platform:..."
	if strings.HasPrefix(key, "/") {
		if idx := strings.Index(key, ":"); idx >= 0 {
			key = key[idx+1:]
		}
	}
	// Format: "platform:channelID:userID" or "platform:type:channelID:userID"
	parts := strings.SplitN(key, ":", 5)
	if len(parts) >= 3 && len(parts[1]) == 1 {
		if len(parts) >= 4 {
			return parts[3]
		}
		return ""
	}
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

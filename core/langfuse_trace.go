package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// LangfuseTraceID returns the deterministic Langfuse trace id that
// agent-runtime's exporter derives for a platform message:
//
//	"turn-" + hex(sha256(sessionKey + "\n" + messageID))[:32]
//
// (the first 16 digest bytes → 32 hex characters).
//
// Logging it in the turn summary lets log analysis join turns to their
// Langfuse traces exactly, without fuzzy time/latency matching. Returns ""
// when either part is missing, mirroring the exporter: such turns carry no
// message-ref trace at all.
func LangfuseTraceID(sessionKey, messageID string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	messageID = strings.TrimSpace(messageID)
	if sessionKey == "" || messageID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionKey + "\n" + messageID))
	return "turn-" + hex.EncodeToString(sum[:16])
}

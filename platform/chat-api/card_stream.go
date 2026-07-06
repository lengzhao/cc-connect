package chatapi

import "strings"

// Markers mirror core.buildCardContent markdown layout (platform-local parser).
const (
	streamThinkingHeader = "💭 **Thinking**\n\n"
	streamSectionBreak   = "\n\n---\n\n"
	streamToolMarker     = "🔧 **Tool #"
)

// parseStreamingCardContent splits Engine streaming-card markdown into thinking
// and answer text. Tool blocks are omitted from both streams.
func parseStreamingCardContent(content string) (thinking, answer string) {
	body := content
	if strings.HasPrefix(body, streamThinkingHeader) {
		body = body[len(streamThinkingHeader):]
		if idx := strings.Index(body, streamSectionBreak); idx >= 0 {
			thinking = strings.TrimSpace(body[:idx])
			body = body[idx+len(streamSectionBreak):]
		} else {
			return strings.TrimSpace(body), ""
		}
	}
	body = strings.TrimSpace(body)
	for {
		trimmed := strings.TrimSpace(body)
		switch {
		case strings.HasPrefix(trimmed, streamSectionBreak):
			body = trimmed[len(streamSectionBreak):]
			continue
		case strings.HasPrefix(trimmed, "---\n\n"):
			body = trimmed[len("---\n\n"):]
			continue
		default:
			body = trimmed
		}
		break
	}
	for body != "" && strings.Contains(body, streamToolMarker) {
		idx := strings.LastIndex(body, streamSectionBreak)
		if idx < 0 {
			return thinking, ""
		}
		body = strings.TrimSpace(body[idx+len(streamSectionBreak):])
	}
	if idx := strings.LastIndex(body, streamSectionBreak); idx >= 0 {
		answer = strings.TrimSpace(body[idx+len(streamSectionBreak):])
	} else {
		answer = body
	}
	return thinking, answer
}

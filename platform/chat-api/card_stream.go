package chatapi

import (
	"regexp"
	"strconv"
	"strings"
)

// Markers mirror core.buildCardContent markdown layout (platform-local parser).
const (
	streamThinkingHeader = "💭 **Thinking**\n\n"
	streamSectionBreak   = "\n\n---\n\n"
	streamToolMarker     = "🔧 **Tool #"
	toolResultMarker     = "🧾"
)

var (
	toolHeaderRe = regexp.MustCompile("(?s)^🔧 \\*\\*Tool #(\\d+)\\*\\*: `([^`]+)`\\n?")
	codeFenceRe  = regexp.MustCompile("(?s)^```[a-zA-Z0-9_-]*\n(.*)\n```$")
)

type streamToolCall struct {
	ID    string
	Name  string
	Input string
}

type streamToolResult struct {
	Name     string
	Status   string
	ExitCode *int
	Success  *bool
	Output   string
}

// parseStreamingCardContent splits Engine streaming-card markdown into thinking
// and answer text. Tool blocks are omitted from both streams.
//
// The answer is isolated structurally — strip the thinking section at the first
// section break, skip any leading tool blocks by their real block boundaries,
// and treat everything that remains as the answer. It deliberately does NOT use
// LastIndex on the "\n\n---\n\n" separator: the answer body may contain its own
// "---" markdown horizontal rules, and splitting on the last one truncated the
// answer to only its final paragraph.
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
	// Strip the "---\n\n" the Engine writes before the answer (and any leading
	// section breaks), then skip contiguous leading tool blocks.
	body = stripLeadingSeparators(body)
	for strings.HasPrefix(body, streamToolMarker) {
		body = stripLeadingSeparators(body[toolBlockEnd(body):])
	}
	// NOTE: a tool *input* that itself contains "\n\n---\n\n" can still cut a
	// block early — acceptable pre-existing limitation, not worth expanding
	// scope for.
	return thinking, strings.TrimSpace(body)
}

// stripLeadingSeparators trims whitespace and removes any leading section-break
// / "---\n\n" separators from body.
func stripLeadingSeparators(body string) string {
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
	return body
}

// toolBlockEnd returns the end offset of one 🔧 tool block within block, which
// must start at a streamToolMarker. A block ends at the min of the next tool
// marker, the next section break, or a single-line "\n---\n" rule.
func toolBlockEnd(block string) int {
	end := len(block)
	if next := strings.Index(block[len(streamToolMarker):], streamToolMarker); next >= 0 {
		end = len(streamToolMarker) + next
	}
	if br := strings.Index(block, streamSectionBreak); br >= 0 && br < end {
		end = br
	}
	if br := strings.Index(block, "\n---\n"); br >= 0 && br < end {
		end = br
	}
	return end
}

// extractStreamingToolCalls finds 🔧 Tool #N blocks from Engine card markdown.
func extractStreamingToolCalls(content string) []streamToolCall {
	var out []streamToolCall
	search := content
	for {
		idx := strings.Index(search, streamToolMarker)
		if idx < 0 {
			break
		}
		chunk := search[idx:]
		end := toolBlockEnd(chunk)
		block := strings.TrimSpace(chunk[:end])
		if tc, ok := parseOneToolCallBlock(block); ok {
			out = append(out, tc)
		}
		search = chunk[end:]
	}
	return out
}

func parseOneToolCallBlock(block string) (streamToolCall, bool) {
	m := toolHeaderRe.FindStringSubmatch(block)
	if m == nil {
		return streamToolCall{}, false
	}
	tc := streamToolCall{
		ID:   m[1],
		Name: strings.TrimSpace(m[2]),
	}
	rest := strings.TrimSpace(block[len(m[0]):])
	tc.Input = stripCodeFence(rest)
	return tc, true
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if m := codeFenceRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	// Short form: `arg`
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' && !strings.Contains(s[1:len(s)-1], "`") {
		return s[1 : len(s)-1]
	}
	return s
}

// parseToolResultFallback detects Engine formatToolResultEventFallback markdown.
func parseToolResultFallback(content string) (streamToolResult, bool) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, toolResultMarker) {
		return streamToolResult{}, false
	}
	lines := strings.Split(trimmed, "\n")
	var res streamToolResult
	first := strings.TrimSpace(lines[0])
	if name := strings.TrimSpace(strings.TrimPrefix(first, toolResultMarker)); name != "" {
		res.Name = name
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "🟢"), strings.HasPrefix(line, "🔴"), strings.HasPrefix(line, "⚪"):
			if strings.HasPrefix(line, "🟢") {
				ok := true
				res.Success = &ok
			} else if strings.HasPrefix(line, "🔴") {
				ok := false
				res.Success = &ok
			}
			if idx := strings.Index(line, ": "); idx >= 0 {
				res.Status = strings.TrimSpace(line[idx+2:])
			}
		case strings.HasPrefix(line, "🔢"):
			if idx := strings.Index(line, ": "); idx >= 0 {
				if n, err := strconv.Atoi(strings.TrimSpace(line[idx+2:])); err == nil {
					res.ExitCode = &n
				}
			}
		default:
			outputBlock := strings.TrimSpace(strings.Join(lines[i:], "\n"))
			if strings.HasPrefix(outputBlock, "_") && strings.HasSuffix(outputBlock, "_") &&
				!strings.Contains(outputBlock[1:len(outputBlock)-1], "\n") {
				return res, true
			}
			res.Output = stripCodeFence(outputBlock)
			return res, true
		}
	}
	return res, true
}

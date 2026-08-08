package chatapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const (
	toolSSEEmitThinking   = "thinking"
	toolSSEEmitClientFlow = "client_flow"

	toolSSEWhenCall   = "tool_call"
	toolSSEWhenResult = "tool_result"
)

type toolSSETransformRule struct {
	Tool           string            `json:"tool"`
	Emit           string            `json:"emit"`
	When           string            `json:"when"`
	Suppress       bool              `json:"suppress"`
	FlowType       string            `json:"flow_type"`
	ArgsFrom       string            `json:"args_from"`
	ArgsFromResult string            `json:"args_from_result"` // alias of args_from
	Text           map[string]string `json:"text"`
	toolLower      string
}

type toolSSETransformFile struct {
	Default    *toolSSETransformRule  `json:"default"`
	Transforms []toolSSETransformRule `json:"transforms"`
}

type toolSSETransformRegistry struct {
	byTool      map[string]toolSSETransformRule
	defaultRule *toolSSETransformRule
}

func loadToolSSETransforms(path string) (*toolSSETransformRegistry, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("chat-api: tool_sse_transforms_file: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("chat-api: tool_sse_transforms_file %q: %w", abs, err)
	}
	var file toolSSETransformFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("chat-api: tool_sse_transforms_file %q: parse: %w", abs, err)
	}
	reg := &toolSSETransformRegistry{byTool: map[string]toolSSETransformRule{}}
	if file.Default != nil {
		norm, err := normalizeToolSSETransformDefault(*file.Default)
		if err != nil {
			return nil, fmt.Errorf("chat-api: tool_sse_transforms_file %q: default: %w", abs, err)
		}
		reg.defaultRule = &norm
	}
	for i, rule := range file.Transforms {
		norm, err := normalizeToolSSETransformRule(rule, i)
		if err != nil {
			return nil, fmt.Errorf("chat-api: tool_sse_transforms_file %q: transforms[%d]: %w", abs, i, err)
		}
		if _, exists := reg.byTool[norm.toolLower]; exists {
			return nil, fmt.Errorf("chat-api: tool_sse_transforms_file %q: duplicate tool %q", abs, norm.Tool)
		}
		reg.byTool[norm.toolLower] = norm
	}
	if reg.defaultRule == nil && len(reg.byTool) == 0 {
		return reg, nil
	}
	return reg, nil
}

func normalizeToolSSETransformDefault(rule toolSSETransformRule) (toolSSETransformRule, error) {
	return normalizeToolSSETransformRuleBody(rule)
}

func normalizeToolSSETransformRule(rule toolSSETransformRule, index int) (toolSSETransformRule, error) {
	rule.Tool = strings.TrimSpace(rule.Tool)
	if rule.Tool == "" {
		return toolSSETransformRule{}, fmt.Errorf("tool is required")
	}
	norm, err := normalizeToolSSETransformRuleBody(rule)
	if err != nil {
		return toolSSETransformRule{}, err
	}
	norm.toolLower = strings.ToLower(norm.Tool)
	_ = index
	return norm, nil
}

func normalizeToolSSETransformRuleBody(rule toolSSETransformRule) (toolSSETransformRule, error) {
	rule.Emit = strings.ToLower(strings.TrimSpace(rule.Emit))
	rule.When = strings.ToLower(strings.TrimSpace(rule.When))
	if rule.When == "" {
		rule.When = toolSSEWhenCall
	}
	switch rule.When {
	case toolSSEWhenCall, toolSSEWhenResult:
	default:
		return toolSSETransformRule{}, fmt.Errorf("when must be tool_call or tool_result, got %q", rule.When)
	}

	argsFrom := strings.TrimSpace(rule.ArgsFrom)
	alias := strings.TrimSpace(rule.ArgsFromResult)
	if argsFrom != "" && alias != "" && argsFrom != alias {
		return toolSSETransformRule{}, fmt.Errorf("args_from and args_from_result conflict")
	}
	if argsFrom == "" {
		argsFrom = alias
	}
	rule.ArgsFrom = argsFrom
	rule.ArgsFromResult = ""

	switch rule.Emit {
	case toolSSEEmitThinking:
		if rule.ArgsFrom != "" {
			return toolSSETransformRule{}, fmt.Errorf("args_from is only valid for client_flow emit")
		}
	case toolSSEEmitClientFlow:
		rule.FlowType = core.NormalizeAskUserEvent(rule.FlowType)
		if rule.FlowType == "" {
			return toolSSETransformRule{}, fmt.Errorf("flow_type is required for client_flow emit")
		}
		if rule.ArgsFrom != "" {
			if _, err := normalizeArgsFromPath(rule.ArgsFrom); err != nil {
				return toolSSETransformRule{}, err
			}
		}
	default:
		return toolSSETransformRule{}, fmt.Errorf("emit must be thinking or client_flow, got %q", rule.Emit)
	}
	if len(rule.Text) == 0 {
		return toolSSETransformRule{}, fmt.Errorf("text map is required")
	}
	clean := make(map[string]string, len(rule.Text))
	for k, v := range rule.Text {
		tag := normalizeTransformLocale(k)
		text := strings.TrimSpace(v)
		if tag == "" || text == "" {
			continue
		}
		clean[tag] = text
	}
	if len(clean) == 0 {
		return toolSSETransformRule{}, fmt.Errorf("text map has no non-empty entries")
	}
	rule.Text = clean
	return rule, nil
}

// normalizeArgsFromPath accepts object-field paths like $.task_id,
// $.data.task_id, or data.task_id. Array indexes / filters are not supported.
func normalizeArgsFromPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("args_from path is empty")
	}
	segments := splitArgsFromPath(path)
	if len(segments) == 0 {
		return "", fmt.Errorf("args_from path is empty")
	}
	for _, seg := range segments {
		if seg == "" || strings.ContainsAny(seg, "[]*?()") {
			return "", fmt.Errorf("args_from path %q is invalid", path)
		}
	}
	return strings.Join(segments, "."), nil
}

func splitArgsFromPath(path string) []string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}

// extractJSONPathArg walks a simple object-field path in JSON text.
// Returns (value, true) for string/number/bool leaves; otherwise ("", false).
func extractJSONPathArg(raw, path string) (string, bool) {
	segments := splitArgsFromPath(path)
	if len(segments) == 0 {
		return "", false
	}
	var root any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &root); err != nil {
		return "", false
	}
	cur := root
	for _, seg := range segments {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		next, ok := obj[seg]
		if !ok {
			return "", false
		}
		cur = next
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), true
		}
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

func normalizeTransformLocale(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch lower {
	case "en", "en-us", "en_us":
		return "en"
	case "zh", "zh-cn", "zh_cn", "zh-hans":
		return "zh"
	case "zh-tw", "zh_tw", "zh-hant":
		return "zh-TW"
	case "ja", "ja-jp", "ja_jp":
		return "ja"
	case "es", "es-es", "es_es":
		return "es"
	default:
		return raw
	}
}

func (reg *toolSSETransformRegistry) lookup(toolName string) (toolSSETransformRule, bool) {
	if reg == nil {
		return toolSSETransformRule{}, false
	}
	key := strings.ToLower(strings.TrimSpace(toolName))
	if rule, ok := reg.byTool[key]; ok {
		return rule, true
	}
	if reg.defaultRule != nil {
		return *reg.defaultRule, true
	}
	return toolSSETransformRule{}, false
}

func formatTransformText(text map[string]string, language, toolName string) string {
	raw := pickTransformText(text, language)
	if raw == "" {
		return ""
	}
	if toolName != "" {
		raw = strings.ReplaceAll(raw, "{tool}", toolName)
	}
	return raw
}

func pickTransformText(text map[string]string, language string) string {
	if len(text) == 0 {
		return ""
	}
	lang := normalizeTransformLocale(language)
	if lang != "" {
		if s := strings.TrimSpace(text[lang]); s != "" {
			return s
		}
	}
	if s := strings.TrimSpace(text["en"]); s != "" {
		return s
	}
	for _, s := range text {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func resolveRunLanguage(agentLang string) string {
	if lang := normalizeTransformLocale(agentLang); lang != "" {
		return lang
	}
	return "en"
}

func waitingAnswerFlowDescription(language string) string {
	switch normalizeTransformLocale(language) {
	case "zh", "zh-cn", "zh-tw":
		return "有待回复的问题"
	case "ja":
		return "未回答の質問があります"
	case "es":
		return "Tiene una pregunta sin responder"
	default:
		return "You have an unanswered question"
	}
}

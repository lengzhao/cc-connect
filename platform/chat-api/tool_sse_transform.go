package chatapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const (
	toolSSEEmitThinking   = "thinking"
	toolSSEEmitClientFlow = "client_flow"
)

type toolSSETransformRule struct {
	Tool      string            `json:"tool"`
	Emit      string            `json:"emit"`
	Suppress  bool              `json:"suppress"`
	FlowType  string            `json:"flow_type"`
	Text      map[string]string `json:"text"`
	toolLower string
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
	switch rule.Emit {
	case toolSSEEmitThinking:
	case toolSSEEmitClientFlow:
		rule.FlowType = core.NormalizeAskUserEvent(rule.FlowType)
		if rule.FlowType == "" {
			return toolSSETransformRule{}, fmt.Errorf("flow_type is required for client_flow emit")
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

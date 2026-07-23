package askuser

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Known envelope event values for App navigation guidance.
const (
	EventConnectAccount     = core.AskEventConnectAccount
	EventCreateTask         = core.AskEventCreateTask
	EventTaskCenterApproval = core.AskEventTaskCenterApproval
)

// Tag variant enums for option badges.
const (
	TagVariantRecommend = "recommend" // 推荐（绿）
	TagVariantKeep      = "keep"      // 维持（灰）
	TagVariantDefault   = "default"   // 默认（灰）
	TagVariantWarning   = "warning"   // 警告（黄）
)

// NormalizeEvent keeps only known navigation events; empty/null/unmatched → "".
func NormalizeEvent(raw string) string {
	return core.NormalizeAskUserEvent(raw)
}

// NormalizeTagVariant keeps only known badge variants; unmatched → "".
func NormalizeTagVariant(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case TagVariantRecommend, TagVariantKeep, TagVariantDefault, TagVariantWarning:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

// ParseToolArguments maps MCP tool arguments to core.UserQuestion.
func ParseToolArguments(raw json.RawMessage) (core.UserQuestion, error) {
	if len(raw) == 0 {
		return core.UserQuestion{}, fmt.Errorf("arguments required")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return core.UserQuestion{}, fmt.Errorf("invalid arguments: %w", err)
	}
	q := core.UserQuestion{
		Question:         firstString(m, "question", "title", "header"),
		Description:      strField(m, "description"),
		Event:            NormalizeEvent(strField(m, "event")),
		AllowCustomInput: boolField(m, "allow_custom_input") || boolField(m, "allowCustomInput"),
		MultiSelect:      boolField(m, "multi_select") || boolField(m, "multiSelect"),
	}
	if opts, ok := m["options"].([]any); ok {
		for _, o := range opts {
			om, ok := o.(map[string]any)
			if !ok {
				continue
			}
			tag, variant := parseTag(om["tag"])
			q.Options = append(q.Options, core.UserQuestionOption{
				Label:       strField(om, "label"),
				Description: strField(om, "description"),
				Value:       valueString(om["value"]),
				Tag:         tag,
				TagVariant:  variant,
			})
		}
	}
	if q.Question == "" {
		return core.UserQuestion{}, fmt.Errorf("question required")
	}
	if len(q.Options) == 0 {
		return core.UserQuestion{}, fmt.Errorf("options required")
	}
	return q, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := strField(m, k); s != "" {
			return s
		}
	}
	return ""
}

func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func boolField(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true") || t == "1"
	default:
		return false
	}
}

func valueString(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func parseTag(raw any) (text, variant string) {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v), ""
	case map[string]any:
		text = strings.TrimSpace(strField(v, "text"))
		variant = NormalizeTagVariant(strField(v, "variant"))
		return text, variant
	default:
		return "", ""
	}
}

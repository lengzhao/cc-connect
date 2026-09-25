package feishu

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chenhg5/cc-connect/core"
)

// Raw Card 2.0 layout is agent-owned; callbacks and routing are host-owned.
func (p *Platform) PrepareDecisionSpec(s *core.DecisionSpec) error {
	if len(s.Card) == 0 {
		return nil
	}
	if len(s.Card) > 24000 {
		return fmt.Errorf("card exceeds 24KB")
	}
	if len(s.Fields) > 0 || len(s.Options) > 0 {
		return fmt.Errorf("use card OR fields/options, not both")
	}
	var card map[string]any
	if err := json.Unmarshal(s.Card, &card); err != nil {
		return fmt.Errorf("invalid card JSON")
	}
	if card["schema"] != "2.0" {
		return fmt.Errorf("card.schema must be 2.0")
	}
	for k := range card {
		if k != "schema" && k != "header" && k != "body" && k != "config" {
			return fmt.Errorf("unsupported card property %s", k)
		}
	}
	body, ok := card["body"].(map[string]any)
	if !ok {
		return fmt.Errorf("card.body required")
	}
	elements, ok := body["elements"].([]any)
	if !ok {
		return fmt.Errorf("card.body.elements required")
	}
	if s.Title == "" {
		if h, ok := card["header"].(map[string]any); ok {
			s.Title = cardPlainText(h["title"])
		}
	}
	if s.Title == "" {
		s.Title = "Interaction"
	}
	if s.Markdown == "" {
		s.Markdown = s.Title
	}
	s.AllowComment = false
	seen := map[string]bool{"comment": true, "decision_form": true}
	forms, total, outside := 0, 0, 0
	var walk func([]any, int, bool) error
	walk = func(nodes []any, depth int, inForm bool) error {
		if depth > 10 {
			return fmt.Errorf("card nesting exceeds 10 levels")
		}
		for _, raw := range nodes {
			total++
			if total > 150 {
				return fmt.Errorf("card exceeds 150 components")
			}
			node, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("card component must be an object")
			}
			tag, _ := node["tag"].(string)
			if _, exists := node["behaviors"]; exists {
				return fmt.Errorf("Runtime owns card behaviors; omit them")
			}
			switch tag {
			case "markdown", "img", "hr":
			case "form":
				forms++
				if inForm || forms > 1 {
					return fmt.Errorf("use at most one form")
				}
			case "column_set", "column":
			case "button", "input", "select_static", "multi_select_static", "checker", "checkbox":
				if !inForm {
					outside++
				}
				name, _ := node["name"].(string)
				if name == "" || seen[name] {
					return fmt.Errorf("interactive names must be unique and nonempty")
				}
				seen[name] = true
				if tag == "button" {
					for _, k := range []string{"url", "value", "form_action_type"} {
						if _, exists := node[k]; exists {
							return fmt.Errorf("Runtime owns button %s; omit it", k)
						}
					}
					label := cardPlainText(node["text"])
					skip, _ := node["skip_validation"].(bool)
					s.Options = append(s.Options, core.DecisionOption{ID: name, Label: label, SkipValidation: skip})
				} else {
					required, _ := node["required"].(bool)
					f := core.DecisionField{ID: name, Label: name, Required: required, Placeholder: cardPlainText(node["placeholder"])}
					if tag == "checker" || tag == "checkbox" {
						f.Type = "checkbox"
						f.Default = node["checked"]
						if label := cardPlainText(node["text"]); label != "" {
							f.Label = label
						}
					} else if tag == "input" {
						f.Type = "text"
						if node["input_type"] == "multiline_text" {
							f.Type = "textarea"
						}
						f.Default = node["default_value"]
						if n, ok := node["max_length"].(float64); ok {
							f.MaxLength = int(n)
						}
					} else {
						f.Type = "select"
						f.Default = node["initial_option"]
						if tag == "multi_select_static" {
							f.Type = "multiselect"
							f.Default = node["selected_values"]
						}
						if _, bad := node["initial_options"]; bad {
							return fmt.Errorf("multi_select_static defaults use selected_values")
						}
						choices, _ := node["options"].([]any)
						for _, raw := range choices {
							choice, ok := raw.(map[string]any)
							if !ok {
								return fmt.Errorf("invalid choice")
							}
							id, _ := choice["value"].(string)
							f.Options = append(f.Options, core.DecisionOption{ID: id, Label: cardPlainText(choice["text"])})
						}
					}
					s.Fields = append(s.Fields, f)
				}
			default:
				return fmt.Errorf("unsupported card component %q", tag)
			}
			for _, key := range []string{"elements", "columns"} {
				if child, exists := node[key]; exists {
					list, ok := child.([]any)
					if !ok {
						return fmt.Errorf("%s must be an array", key)
					}
					if err := walk(list, depth+1, inForm || tag == "form"); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := walk(elements, 0, false); err != nil {
		return err
	}
	if forms > 0 && outside > 0 {
		return fmt.Errorf("all interactive components must be inside the same form")
	}
	if len(s.Fields) > 0 && len(s.Options) == 0 {
		return fmt.Errorf("input fields require a submission button")
	}
	return nil
}
func cardPlainText(raw any) string {
	if m, ok := raw.(map[string]any); ok {
		s, _ := m["content"].(string)
		return s
	}
	return ""
}

func customDecisionCard(v *core.Decision, receipt bool) map[string]any {
	var card map[string]any
	_ = json.Unmarshal(v.Spec.Card, &card)
	body := card["body"].(map[string]any)
	fields := map[string]core.DecisionField{}
	for _, f := range v.Spec.Fields {
		fields[f.ID] = f
	}
	options := map[string]core.DecisionOption{}
	skip := false
	for _, o := range v.Spec.Options {
		options[o.ID] = o
		skip = skip || o.SkipValidation || o.Cancel
	}
	hasForm := false
	var render func([]any) []any
	render = func(nodes []any) []any {
		out := []any{}
		for _, raw := range nodes {
			node := raw.(map[string]any)
			tag, _ := node["tag"].(string)
			name, _ := node["name"].(string)
			if tag == "form" {
				hasForm = true
				if node["name"] == nil {
					node["name"] = "decision_form"
				}
			}
			if tag == "button" {
				if receipt {
					continue
				}
				o := options[name]
				delete(node, "skip_validation")
				node["form_action_type"] = "submit"
				node["behaviors"] = []any{map[string]any{"type": "callback", "value": map[string]string{"action": "decision:submit", "request_id": v.ID, "option_id": o.ID, "revision": fmt.Sprint(v.Revision)}}}
			}
			if f, ok := fields[name]; ok {
				if receipt {
					copy := *v
					copy.Spec.Fields = []core.DecisionField{f}
					for _, summary := range decisionValuesSummary(&copy) {
						out = append(out, summary)
					}
					continue
				}
				if f.Type == "checkbox" {
					node["tag"] = "checker"
					delete(node, "required") // Checkbox consent is validated on form submit.
				} else {
					node["required"] = f.Required && !skip
				}
				if f.Type == "text" || f.Type == "textarea" {
					node["max_length"] = f.MaxLength
				}
				if f.Default != nil {
					switch f.Type {
					case "text", "textarea":
						node["default_value"] = f.Default
					case "select":
						node["initial_option"] = f.Default
					case "checkbox":
						node["checked"] = f.Default
					case "multiselect":
						node["selected_values"] = f.Default
					}
				}
			}
			empty := false
			for _, key := range []string{"elements", "columns"} {
				if child, ok := node[key].([]any); ok {
					node[key] = render(child)
					if len(node[key].([]any)) == 0 {
						empty = true
					}
				}
			}
			if empty && (tag == "column" || tag == "column_set" || tag == "form") {
				continue
			}
			if receipt && tag == "form" {
				if child, ok := node["elements"].([]any); ok {
					out = append(out, child...)
				}
				continue
			}
			out = append(out, node)
		}
		return out
	}
	nodes := render(body["elements"].([]any))
	if !receipt && !hasForm && len(v.Spec.Options) > 0 {
		nodes = []any{map[string]any{"tag": "form", "name": "decision_form", "elements": nodes}}
	}
	if receipt {
		label := v.OptionID
		for _, o := range v.Spec.Options {
			if o.ID == v.OptionID {
				label = o.Label
			}
		}
		i := core.NewI18n(core.DetectLanguage(v.Spec.Title + v.Spec.Markdown))
		nodes = append(nodes, map[string]any{"tag": "markdown", "content": "**✓ " + escapeDecisionMarkdown(label) + "**\n" + i.T(core.MsgDecisionSaved)})
	}
	body["elements"] = nodes
	config, ok := card["config"].(map[string]any)
	if !ok {
		config = map[string]any{}
		card["config"] = config
	}
	config["update_multi"] = true
	return card
}

func (p *Platform) RecordDecisionReceipt(ctx context.Context, v *core.Decision) error {
	if p.client == nil {
		return fmt.Errorf("card client unavailable")
	}
	return p.patchDecisionCard(ctx, v.MessageID, decisionCard(v, true))
}

package feishu

import (
	"fmt"
	"github.com/chenhg5/cc-connect/core"
	"strings"
)

// Native form controls share extraction, defaults and receipt handling. Keep this
// contract separate from visual layouts: accepting a tag alone is not enough.
type decisionComponent struct {
	fieldType, defaultKey string
	choices               bool
}

var decisionComponents = map[string]decisionComponent{
	"input":               {"text", "default_value", false},
	"textarea":            {"textarea", "default_value", false},
	"checker":             {"checkbox", "checked", false},
	"checkbox":            {"checkbox", "checked", false},
	"select_static":       {"select", "initial_option", true},
	"multi_select_static": {"multiselect", "selected_values", true},
	"select_person":       {"person", "initial_option", true},
	"multi_select_person": {"people", "selected_values", true},
	"date_picker":         {"date", "initial_date", false},
	"picker_time":         {"time", "initial_time", false},
	"picker_datetime":     {"datetime", "initial_datetime", false},
	"select_img":          {"images", "", true},
}

func decisionFieldFromComponent(node map[string]any) (core.DecisionField, error) {
	tag, _ := node["tag"].(string)
	d := decisionComponents[tag]
	name, _ := node["name"].(string)
	required, _ := node["required"].(bool)
	f := core.DecisionField{ID: name, Label: name, Type: d.fieldType, Required: required, Placeholder: cardPlainText(node["placeholder"])}
	if label := cardPlainText(node["label"]); label != "" {
		f.Label = label
	}
	if d.defaultKey != "" {
		f.Default = node[d.defaultKey]
	}
	switch tag {
	case "input", "textarea":
		if tag == "textarea" || node["input_type"] == "multiline_text" {
			f.Type = "textarea"
		}
		if typ, ok := node["input_type"]; ok && typ != "text" && typ != "multiline_text" && typ != "password" {
			return f, fmt.Errorf("unsupported input_type %v", typ)
		}
		if n, ok := node["max_length"].(float64); ok {
			if n != float64(int(n)) || n < 1 || n > 1000 {
				return f, fmt.Errorf("input max_length must be 1–1000")
			}
			f.MaxLength = int(n)
		}
	case "checker", "checkbox":
		if label := cardPlainText(node["text"]); label != "" {
			f.Label = label
		}
	case "select_img":
		if node["multi_select"] != true {
			f.Type = "image"
		}
	}
	if d.choices {
		if _, bad := node["initial_options"]; bad {
			return f, fmt.Errorf("multi-select defaults use selected_values")
		}
		choices, _ := node["options"].([]any)
		for _, raw := range choices {
			choice, ok := raw.(map[string]any)
			if !ok {
				return f, fmt.Errorf("invalid choice")
			}
			id, _ := choice["value"].(string)
			label := cardPlainText(choice["text"])
			if label == "" {
				label = id
			}
			f.Options = append(f.Options, core.DecisionOption{ID: id, Label: label})
		}
	}
	return f, nil
}
func decisionChildrenKey(tag string) string {
	switch tag {
	case "column_set":
		return "columns"
	case "column", "form", "collapsible_panel":
		return "elements"
	}
	return ""
}
func normalizeDecisionInput(node map[string]any, f core.DecisionField) {
	tag, _ := node["tag"].(string)
	if tag == "textarea" {
		node["tag"] = "input"
		node["input_type"] = "multiline_text"
	}
	if f.Type == "textarea" {
		node["input_type"] = "multiline_text"
	}
	if f.Type == "checkbox" {
		node["tag"] = "checker"
		delete(node, "required")
	}
	if f.Type == "text" || f.Type == "textarea" {
		node["max_length"] = f.MaxLength
	}
	if f.Default != nil {
		if key := decisionComponents[tag].defaultKey; key != "" {
			node[key] = decisionDefaultValue(f)
		}
	}
}

func decisionInteractiveSpan(nodes []any) (int, int) {
	first, last := -1, -1
	var interactive func(map[string]any) bool
	interactive = func(node map[string]any) bool {
		tag, _ := node["tag"].(string)
		if tag == "button" {
			return true
		}
		if _, ok := decisionComponents[tag]; ok {
			return true
		}
		if key := decisionChildrenKey(tag); key != "" {
			if children, ok := node[key].([]any); ok {
				for _, c := range children {
					if interactive(c.(map[string]any)) {
						return true
					}
				}
			}
		}
		return false
	}
	for n, raw := range nodes {
		if interactive(raw.(map[string]any)) {
			if first < 0 {
				first = n
			}
			last = n
		}
	}
	return first, last
}

// Picker callbacks include an offset, while initial_* accepts local date/time only.
func decisionDefaultValue(f core.DecisionField) any {
	if f.Type == "date" || f.Type == "time" || f.Type == "datetime" {
		if value, ok := f.Default.(string); ok {
			if n := strings.LastIndex(value, " "); n >= 0 {
				offset := value[n+1:]
				if len(offset) == 5 && (offset[0] == '+' || offset[0] == '-') {
					return value[:n]
				}
			}
		}
	}
	return f.Default
}

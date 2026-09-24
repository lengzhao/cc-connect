package feishu

import (
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func decisionFormFields(fields []core.DecisionField) []map[string]any {
	out := []map[string]any{}
	for _, f := range fields {
		out = append(out, map[string]any{"tag": "markdown", "content": "**" + escapeDecisionMarkdown(f.Label) + "**"})
		c := map[string]any{"name": f.ID, "required": f.Required, "placeholder": plainText(f.Placeholder), "width": "fill"}
		switch f.Type {
		case "text", "textarea":
			c["tag"] = "input"
			c["max_length"] = f.MaxLength
			if f.Type == "textarea" {
				c["input_type"] = "multiline_text"
				c["rows"] = 3
			}
			if f.Default != nil {
				c["default_value"] = f.Default
			}
		case "select", "multiselect":
			c["tag"] = "select_static"
			if f.Type == "multiselect" {
				c["tag"] = "multi_select_static"
			}
			options := []any{}
			for _, o := range f.Options {
				options = append(options, map[string]any{"text": plainText(o.Label), "value": o.ID})
			}
			c["options"] = options
			if f.Default != nil {
				if f.Type == "select" {
					c["initial_option"] = f.Default
				} else {
					// Card 2.0 multi-select uses selected_values, not initial_options.
					c["selected_values"] = f.Default
				}
			}
		}
		out = append(out, c)
	}
	return out
}
func decisionValuesSummary(v *core.Decision) []map[string]any {
	out := []map[string]any{}
	for _, f := range v.Spec.Fields {
		raw, ok := v.Values[f.ID]
		if !ok {
			continue
		}
		labels := map[string]string{}
		for _, o := range f.Options {
			labels[o.ID] = o.Label
		}
		show := func(s string) string {
			if label, ok := labels[s]; ok {
				return label
			}
			return s
		}
		text := ""
		switch value := raw.(type) {
		case string:
			text = show(value)
		case []string:
			a := []string{}
			for _, x := range value {
				a = append(a, show(x))
			}
			text = strings.Join(a, ", ")
		case []any:
			a := []string{}
			for _, x := range value {
				a = append(a, show(fmt.Sprint(x)))
			}
			text = strings.Join(a, ", ")
		}
		out = append(out, map[string]any{"tag": "markdown", "content": "**" + escapeDecisionMarkdown(f.Label) + "**: " + escapeDecisionMarkdown(text)})
	}
	return out
}

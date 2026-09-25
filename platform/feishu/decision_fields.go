package feishu

import (
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func decisionFormFields(fields []core.DecisionField) []map[string]any {
	out := []map[string]any{}
	for _, f := range fields {
		if f.Type != "checkbox" {
			out = append(out, map[string]any{"tag": "markdown", "content": "**" + escapeDecisionMarkdown(f.Label) + "**"})
		}
		c := map[string]any{"name": f.ID, "required": f.Required, "placeholder": plainText(f.Placeholder), "width": "fill"}
		switch f.Type {
		case "checkbox":
			c = map[string]any{"tag": "checker", "name": f.ID, "text": plainText(f.Label), "checked": false}
			if f.Default != nil {
				c["checked"] = f.Default
			}
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
		case "date", "time", "datetime":
			tag := map[string]string{"date": "date_picker", "time": "picker_time", "datetime": "picker_datetime"}[f.Type]
			c["tag"] = tag
			if f.Default != nil {
				c[decisionComponents[tag].defaultKey] = decisionDefaultValue(f)
			}
		case "person", "people", "select", "multiselect":
			c["tag"] = "select_static"
			if f.Type == "multiselect" || f.Type == "people" {
				c["tag"] = "multi_select_static"
			}
			if f.Type == "person" {
				c["tag"] = "select_person"
			}
			if f.Type == "people" {
				c["tag"] = "multi_select_person"
			}
			options := []any{}
			for _, o := range f.Options {
				if f.Type == "person" || f.Type == "people" {
					options = append(options, map[string]any{"value": o.ID})
				} else {
					options = append(options, map[string]any{"text": plainText(o.Label), "value": o.ID})
				}
			}
			c["options"] = options
			if f.Default != nil {
				if f.Type == "select" || f.Type == "person" {
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
		case bool:
			if value {
				text = "☑"
			} else {
				text = "☐"
			}
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

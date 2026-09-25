package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type DecisionFormError struct{ Message string }

func (e *DecisionFormError) Error() string { return e.Message }

var decisionFieldID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func validateDecisionFields(s *DecisionSpec) error {
	if len(s.Fields) > 20 {
		return fmt.Errorf("at most 20 form fields")
	}
	seen := map[string]bool{"comment": true}
	for n := range s.Fields {
		f := &s.Fields[n]
		if !decisionFieldID.MatchString(f.ID) || seen[f.ID] || strings.TrimSpace(f.Label) == "" || len(f.Label) > 200 {
			return fmt.Errorf("invalid or duplicate form field %q", f.ID)
		}
		seen[f.ID] = true
		if len(f.Placeholder) > 500 {
			return fmt.Errorf("field %s: placeholder too long", f.ID)
		}
		switch f.Type {
		case "checkbox":
			if len(f.Options) != 0 {
				return fmt.Errorf("field %s: checkbox cannot have options", f.ID)
			}
		case "text", "textarea":
			if len(f.Options) != 0 {
				return fmt.Errorf("field %s: text cannot have options", f.ID)
			}
			if f.MaxLength == 0 {
				f.MaxLength = 1000
			}
			if f.MaxLength < 1 || f.MaxLength > 1000 {
				return fmt.Errorf("field %s: max_length must be 1–1000", f.ID)
			}
		case "date", "time", "datetime":
			if len(f.Options) != 0 {
				return fmt.Errorf("field %s: date/time cannot have options", f.ID)
			}
		case "person", "people":
			if len(f.Options) > 50 {
				return fmt.Errorf("field %s: at most 50 people", f.ID)
			}
			seenIDs := map[string]bool{}
			for _, o := range f.Options {
				if o.ID == "" || len(o.ID) > 128 || seenIDs[o.ID] {
					return fmt.Errorf("field %s: invalid people", f.ID)
				}
				seenIDs[o.ID] = true
			}
		case "select", "multiselect", "image", "images":
			if len(f.Options) < 1 || len(f.Options) > 50 {
				return fmt.Errorf("field %s: provide 1–50 choices", f.ID)
			}
			opts := map[string]bool{}
			for _, o := range f.Options {
				if o.ID == "" || len(o.ID) > 64 || strings.TrimSpace(o.Label) == "" || len(o.Label) > 200 || opts[o.ID] {
					return fmt.Errorf("field %s: invalid choices", f.ID)
				}
				opts[o.ID] = true
			}
		default:
			return fmt.Errorf("field %s: unsupported type", f.ID)
		}
		if f.Default != nil {
			if _, err := normalizeDecisionValue(*f, f.Default, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeDecisionValue(f DecisionField, raw any, required bool) (any, error) {
	fail := func() (any, error) {
		return nil, &DecisionFormError{Message: fmt.Sprintf("invalid or missing value: %s", f.Label)}
	}
	if f.Type == "checkbox" {
		value := false
		if raw != nil {
			var ok bool
			value, ok = raw.(bool)
			if !ok {
				return fail()
			}
		}
		if required && !value {
			return fail()
		}
		return value, nil
	}
	if f.Type == "multiselect" || f.Type == "people" || f.Type == "image" || f.Type == "images" {
		values := []string{}
		switch v := raw.(type) {
		case nil:
		case []string:
			values = v
		case []any:
			for _, x := range v {
				s, ok := x.(string)
				if !ok {
					return fail()
				}
				values = append(values, s)
			}
		default:
			return fail()
		}
		if len(values) > 50 || (f.Type == "image" && len(values) > 1) {
			return fail()
		}
		if required && len(values) == 0 {
			return fail()
		}
		allowed := map[string]bool{}
		for _, o := range f.Options {
			allowed[o.ID] = true
		}
		selected := map[string]bool{}
		for _, v := range values {
			if v == "" || len(v) > 128 || (!allowed[v] && !(f.Type == "people" && len(f.Options) == 0)) || selected[v] {
				return fail()
			}
			selected[v] = true
		}
		if f.Type == "people" && len(f.Options) == 0 {
			sort.Strings(values)
			return values, nil
		}
		// Stable option order makes repeated callbacks independent of array ordering.
		out := []string{}
		for _, o := range f.Options {
			if selected[o.ID] {
				out = append(out, o.ID)
			}
		}
		return out, nil
	}
	value := ""
	if raw != nil {
		var ok bool
		value, ok = raw.(string)
		if !ok {
			return fail()
		}
	}
	if required && strings.TrimSpace(value) == "" {
		return fail()
	}
	if f.Type == "date" || f.Type == "time" || f.Type == "datetime" {
		if value != "" {
			layout := map[string]string{"date": "2006-01-02", "time": "15:04", "datetime": "2006-01-02 15:04"}[f.Type]
			valid := false
			for _, format := range []string{layout, layout + " -0700"} {
				if _, err := time.Parse(format, value); err == nil {
					valid = true
				}
			}
			if !valid {
				return fail()
			}
		}
		return value, nil
	}
	if f.Type == "person" && len(f.Options) == 0 {
		if len(value) > 128 {
			return fail()
		}
		return value, nil
	}
	if f.Type == "select" || f.Type == "person" {
		if value != "" {
			valid := false
			for _, o := range f.Options {
				valid = valid || o.ID == value
			}
			if !valid {
				return fail()
			}
		}
	} else if utf8.RuneCountInString(value) > f.MaxLength {
		return fail()
	}
	return value, nil
}
func validateDecisionValues(s DecisionSpec, option string, submitted []map[string]any) (map[string]any, error) {
	raw := map[string]any{}
	if len(submitted) > 0 && submitted[0] != nil {
		raw = submitted[0]
	}
	allowed := map[string]bool{}
	for _, f := range s.Fields {
		allowed[f.ID] = true
	}
	for k := range raw {
		if !allowed[k] {
			return nil, fmt.Errorf("unknown form field %q", k)
		}
	}
	if len(s.Fields) == 0 {
		return nil, nil
	}
	skipRequired := false
	for _, o := range s.Options {
		if o.ID == option {
			skipRequired = o.SkipValidation || o.Cancel
		}
	}
	values := map[string]any{}
	for _, f := range s.Fields {
		v, err := normalizeDecisionValue(f, raw[f.ID], f.Required && !skipRequired)
		if err != nil {
			return nil, err
		}
		values[f.ID] = v
	}
	return values, nil
}
func decisionValuesEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

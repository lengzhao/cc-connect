package chatapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func stringOption(opts map[string]any, key, fallback string) string {
	if opts == nil {
		return fallback
	}
	value, ok := opts[key]
	if !ok || value == nil {
		return fallback
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	default:
		return fmt.Sprint(v)
	}
}

func durationOption(opts map[string]any, key string, fallback time.Duration) (time.Duration, error) {
	raw := stringOption(opts, key, "")
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	if d <= 0 {
		return 0, errors.New("duration must be positive")
	}
	return d, nil
}

func intOption(opts map[string]any, key string, fallback int) (int, error) {
	if opts == nil {
		return fallback, nil
	}
	value, ok := opts[key]
	if !ok || value == nil {
		return fallback, nil
	}
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("invalid integer %q: %w", v, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unsupported value type %T", value)
	}
}

func stringSliceOption(opts map[string]any, key string) []string {
	if opts == nil {
		return nil
	}
	raw, ok := opts[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

func userHeaderOption(opts map[string]any) (string, error) {
	header := stringOption(opts, "user_header", defaultUserHeader)
	if header == "" {
		return "", errors.New("chat-api: user_header must not be empty")
	}
	return http.CanonicalHeaderKey(header), nil
}

package chatapi

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

type responseHeaderConfig struct {
	name   string
	value  string // fixed; wins over envKey
	envKey string
}

func (c responseHeaderConfig) enabled() bool {
	return c.name != "" && (c.value != "" || c.envKey != "")
}

func (c responseHeaderConfig) resolvedValue() string {
	if c.value != "" {
		return c.value
	}
	if c.envKey != "" {
		return strings.TrimSpace(os.Getenv(c.envKey))
	}
	return ""
}

func responseHeaderOption(opts map[string]any) (responseHeaderConfig, error) {
	name := strings.TrimSpace(stringOption(opts, "response_header", ""))
	if name == "" {
		return responseHeaderConfig{}, nil
	}
	name = http.CanonicalHeaderKey(name)
	if name == "" {
		return responseHeaderConfig{}, errors.New("chat-api: response_header must not be empty")
	}

	value := strings.TrimSpace(stringOption(opts, "response_header_value", ""))
	envKey := strings.TrimSpace(stringOption(opts, "response_header_env", ""))
	if value == "" && envKey == "" {
		return responseHeaderConfig{}, errors.New("chat-api: response_header requires response_header_value or response_header_env")
	}
	return responseHeaderConfig{name: name, value: value, envKey: envKey}, nil
}

func (p *Platform) setResponseHeader(w http.ResponseWriter) {
	if !p.responseHeader.enabled() {
		return
	}
	if v := p.responseHeader.resolvedValue(); v != "" {
		w.Header().Set(p.responseHeader.name, v)
	}
}

func (p *Platform) logResponseHeaderConfig() {
	if !p.responseHeader.enabled() {
		return
	}
	if v := p.responseHeader.resolvedValue(); v == "" {
		slog.Warn("chat-api: response_header configured but value is empty",
			"header", p.responseHeader.name,
			"env", p.responseHeader.envKey)
		return
	}
	slog.Info("chat-api: response_header enabled",
		"header", p.responseHeader.name,
		"env", p.responseHeader.envKey,
		"fixed_value", p.responseHeader.value != "")
}

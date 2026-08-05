package chatapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"text/template"
)

//go:embed debugui/index.html
var debugUIFS embed.FS

type debugUIJSConfig struct {
	APIPath            string                     `json:"apiPath"`
	UserHeader         string                     `json:"userHeader"`
	UserNameHeader     string                     `json:"userNameHeader"`
	ChannelHeader      string                     `json:"channelHeader"`
	AgentContextFields []debugUIAgentContextField `json:"agentContextFields"`
	CustomHeaderSlots  []int                      `json:"customHeaderSlots"`
}

const debugUICustomHeaderSlots = 2

type debugUIAgentContextField struct {
	InputID    string `json:"inputId"`
	HeaderName string `json:"headerName"`
	DefaultVal string `json:"defaultVal,omitempty"`
}

type debugUITemplateData struct {
	UserHeader         string
	UserNameHeader     string
	ChannelHeader      string
	AgentContextFields []debugUIAgentContextField
	CustomHeaderSlots  []int
	Config             debugUIJSConfig
}

var (
	debugUITemplate     *template.Template
	debugUITemplateOnce sync.Once
	debugUITemplateErr  error
)

func loadDebugUITemplate() (*template.Template, error) {
	debugUITemplateOnce.Do(func() {
		debugUITemplate, debugUITemplateErr = template.New("index.html").Funcs(template.FuncMap{
			"json": func(v any) (string, error) {
				b, err := json.Marshal(v)
				if err != nil {
					return "", err
				}
				return string(b), nil
			},
		}).ParseFS(debugUIFS, "debugui/index.html")
	})
	return debugUITemplate, debugUITemplateErr
}

func debugUICustomHeaderSlotIndexes() []int {
	out := make([]int, debugUICustomHeaderSlots)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func (p *Platform) buildDebugUIConfig() debugUIJSConfig {
	return debugUIJSConfig{
		APIPath:            p.path,
		UserHeader:         p.userHeader,
		UserNameHeader:     p.userNameHeader,
		ChannelHeader:      p.channelHeader,
		AgentContextFields: debugUIAgentContextFields(p.agentContextHeaders),
		CustomHeaderSlots:  debugUICustomHeaderSlotIndexes(),
	}
}

func debugUIAgentContextFields(m agentContextHeaderMap) []debugUIAgentContextField {
	type preset struct {
		field      string
		inputID    string
		headerName string
		defaultVal string
	}
	presets := []preset{
		{field: "language", inputID: "lang", headerName: "X-Language", defaultVal: "zh"},
		{field: "task_id", inputID: "taskId", headerName: "X-Task-ID", defaultVal: "ui-job-1"},
		{field: "trace_id", inputID: "traceId", headerName: "X-Trace-ID"},
		{field: "custom.tenant_id", inputID: "tenantId", headerName: "X-Tenant-ID", defaultVal: "acme"},
	}
	if len(m) == 0 {
		out := make([]debugUIAgentContextField, 0, len(presets))
		for _, p := range presets {
			out = append(out, debugUIAgentContextField{
				InputID:    p.inputID,
				HeaderName: p.headerName,
				DefaultVal: p.defaultVal,
			})
		}
		return out
	}

	fields := make([]string, 0, len(m))
	for field := range m {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	presetByField := make(map[string]preset, len(presets))
	for _, p := range presets {
		presetByField[p.field] = p
	}

	out := make([]debugUIAgentContextField, 0, len(fields))
	for _, field := range fields {
		headerName := m[field]
		if preset, ok := presetByField[field]; ok {
			out = append(out, debugUIAgentContextField{
				InputID:    preset.inputID,
				HeaderName: headerName,
				DefaultVal: preset.defaultVal,
			})
			continue
		}
		out = append(out, debugUIAgentContextField{
			InputID:    agentContextFieldInputID(field),
			HeaderName: headerName,
		})
	}
	return out
}

func agentContextFieldInputID(field string) string {
	switch field {
	case "language":
		return "lang"
	case "task_id":
		return "taskId"
	case "trace_id":
		return "traceId"
	default:
		return strings.NewReplacer(".", "_", "-", "_").Replace(field)
	}
}

func (p *Platform) buildDebugUIPage() ([]byte, error) {
	tmpl, err := loadDebugUITemplate()
	if err != nil {
		return nil, err
	}
	cfg := p.buildDebugUIConfig()
	data := debugUITemplateData{
		UserHeader:         p.userHeader,
		UserNameHeader:     p.userNameHeader,
		ChannelHeader:      p.channelHeader,
		AgentContextFields: cfg.AgentContextFields,
		CustomHeaderSlots:  cfg.CustomHeaderSlots,
		Config:             cfg,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (p *Platform) registerDebugUI(mux *http.ServeMux) {
	if !p.debugUI {
		return
	}
	page, err := p.buildDebugUIPage()
	if err != nil {
		return
	}

	serve := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, http.StatusMethodNotAllowed, "invalid request")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	}

	mux.HandleFunc("/debug", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/debug/", http.StatusFound)
	})
	mux.HandleFunc("/debug/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/debug/", "/debug/index.html":
			serve(w, r)
		case "/debug/config.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"api_path":              p.path,
				"user_header":           p.userHeader,
				"user_name_header":      p.userNameHeader,
				"channel_header":        p.channelHeader,
				"agent_context_headers": p.agentContextHeaders,
				"forward_headers":       p.forwardHeaders,
			})
		default:
			http.NotFound(w, r)
		}
	})
}

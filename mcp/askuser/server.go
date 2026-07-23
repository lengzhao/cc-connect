// Package askuser implements a minimal Streamable HTTP MCP server that exposes
// cc_connect_ask_user for Claude Code structured confirmations.
package askuser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	serverName    = "ccconnect"
	protocolVers  = "2024-11-05"
	toolName      = core.ToolCCConnectAskUser
	defaultListen = "127.0.0.1:0"
)

// Server is a resident MCP HTTP endpoint bound to an AskUserHub.
type Server struct {
	hub     *core.AskUserHub
	http    *http.Server
	ln      net.Listener
	baseURL string
	mu      sync.Mutex
}

// Start listens on 127.0.0.1:0 and serves /mcp.
func Start(hub *core.AskUserHub) (*Server, error) {
	return StartOn(hub, defaultListen)
}

// StartOn listens on addr (e.g. "127.0.0.1:0").
func StartOn(hub *core.AskUserHub, addr string) (*Server, error) {
	if hub == nil {
		return nil, fmt.Errorf("askuser: hub is nil")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("askuser: listen: %w", err)
	}
	s := &Server{hub: hub, ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.http = &http.Server{Handler: mux}
	s.baseURL = "http://" + ln.Addr().String()
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("askuser mcp: serve error", "error", err)
		}
	}()
	slog.Info("askuser mcp: listening", "url", s.MCPURL())
	return s, nil
}

// MCPURL is the Streamable HTTP endpoint Claude should call.
func (s *Server) MCPURL() string {
	if s == nil {
		return ""
	}
	return s.baseURL + "/mcp"
}

// BaseURL without path.
func (s *Server) BaseURL() string {
	if s == nil {
		return ""
	}
	return s.baseURL
}

// Close shuts down the HTTP server.
func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Stateless server: no standalone SSE stream.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	case http.MethodPost:
		s.handlePOST(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) handlePOST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	sessionKey := strings.TrimSpace(r.Header.Get(core.SessionKeyHeader))

	// Notifications have no id — acknowledge with 202.
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	if req.Method == "" {
		writeRPCError(w, req.ID, -32600, "invalid request")
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// notification
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": protocolVers,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": "1.0.0"},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": []any{toolDescriptor()}}
	case "tools/call":
		result, err = s.callTool(r.Context(), sessionKey, req.Params)
		if err != nil {
			writeRPCError(w, req.ID, -32000, err.Error())
			return
		}
	default:
		writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
		return
	}
	writeRPCResult(w, req.ID, result)
}

func toolDescriptor() map[string]any {
	return map[string]any{
		"name": toolName,
		"description": "向用户发起结构化单选/多选确认（可选自定义输入）。请用本工具替代 Claude 内置 AskUserQuestion，" +
			"以便保留 event / value / tag / allow_custom_input 等扩展字段。每次只问一个问题。",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"question", "options"},
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "问题标题（必填），展示在确认卡片上。",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "问题补充说明（可选），展示在标题下方。",
				},
				"event": map[string]any{
					"type": "string",
					"description": "发送事件，引导应用进行页面跳转（可选）。" +
						"枚举：connect_account（连接账户）、create_task（创建任务）、task_center_approval（去任务中心审批）。" +
						"缺省、空、null 或未匹配时不渲染额外按钮，走通用发送/确认逻辑。",
					"enum": []any{
						EventConnectAccount,
						EventCreateTask,
						EventTaskCenterApproval,
						"",
					},
				},
				"allow_custom_input": map[string]any{
					"type":        "boolean",
					"description": "是否允许用户输入列表以外的自定义内容。仅当确实接受自由输入时设为 true。",
				},
				"multi_select": map[string]any{
					"type":        "boolean",
					"description": "是否多选。默认 false（单选）。",
				},
				"options": map[string]any{
					"type":        "array",
					"description": "可选项列表（至少一项）。",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"label"},
						"properties": map[string]any{
							"label": map[string]any{
								"type":        "string",
								"description": "选项展示文案（必填）。",
							},
							"description": map[string]any{
								"type":        "string",
								"description": "选项说明（可选）。",
							},
							"value": map[string]any{
								"description": "稳定回传值（可选）。字符串或数字；缺省时用 label。",
							},
							"tag": map[string]any{
								"type":        "object",
								"description": "选项角标（可选）。用于突出推荐/警告等语义。",
								"properties": map[string]any{
									"text": map[string]any{
										"type":        "string",
										"description": "角标文案，如「推荐」「交易所」。",
									},
									"variant": map[string]any{
										"type": "string",
										"description": "角标样式枚举：" +
											"recommend=推荐（绿）；keep=维持（灰）；default=默认（灰）；warning=警告（黄）。",
										"enum": []any{
											TagVariantRecommend,
											TagVariantKeep,
											TagVariantDefault,
											TagVariantWarning,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, sessionKey string, paramsRaw json.RawMessage) (any, error) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if params.Name != toolName && params.Name != core.MCPQualifiedAskUserTool {
		return nil, fmt.Errorf("unknown tool %q", params.Name)
	}
	if sessionKey == "" {
		return nil, fmt.Errorf("missing %s header", core.SessionKeyHeader)
	}
	q, err := ParseToolArguments(params.Arguments)
	if err != nil {
		return nil, err
	}
	// Allow long user think time; client MCP timeout should be raised in mcp config.
	askCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	res, err := s.hub.Ask(askCtx, sessionKey, q)
	if err != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "ask failed: " + err.Error()}},
			"isError": true,
		}, nil
	}
	if res.Denied {
		msg := res.Message
		if msg == "" {
			msg = "user denied"
		}
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": msg}},
			"isError": true,
		}, nil
	}
	text := formatAskResult(q, res)
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	}, nil
}

func formatAskResult(q core.UserQuestion, res core.AskUserResult) string {
	ans := res.Answers[0]
	disp := res.DisplayAnswers[0]
	if disp == "" {
		disp = ans
	}
	return fmt.Sprintf("User answered %q = %q (value=%q).", q.Question, disp, ans)
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRawOrNull(id),
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRawOrNull(id),
		"error":   map[string]any{"code": code, "message": message},
	})
}

func jsonRawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return nil
	}
	return v
}

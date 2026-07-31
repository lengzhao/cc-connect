// Package askuser implements a minimal Streamable HTTP MCP server that exposes
// cc_connect_ask_user and cc_connect_client_flow for Claude Code.
package askuser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	serverName        = "ccconnect"
	protocolVers      = "2024-11-05"
	toolName          = core.ToolCCConnectAskUser
	defaultListen     = "127.0.0.1:0"
	askUserMCPSocket  = "askuser-mcp.sock"
	unixMCPHost       = "http://localhost"
	maxUnixSocketPath = 100 // conservative limit for macOS sun_path
)

// ResolveSocketPath returns the Unix socket path for the resident ask-user MCP server.
// When configured is non-empty it is used as-is; otherwise the default is
// <runDir>/askuser-mcp.sock with a short temp fallback when that path is too long.
func ResolveSocketPath(runDir, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if len(configured) > maxUnixSocketPath {
			return "", fmt.Errorf("askuser: configured socket path too long (%d > %d): %q", len(configured), maxUnixSocketPath, configured)
		}
		return configured, nil
	}
	primary := filepath.Join(runDir, askUserMCPSocket)
	if len(primary) <= maxUnixSocketPath {
		return primary, nil
	}
	sum := sha256.Sum256([]byte(primary))
	short := filepath.Join(os.TempDir(), "cc-connect-askuser-"+hex.EncodeToString(sum[:8])+".sock")
	if len(short) > maxUnixSocketPath {
		return "", fmt.Errorf("askuser: cannot derive unix socket path under %q", runDir)
	}
	slog.Warn("askuser mcp: run_dir path too long for askuser-mcp.sock, using temp socket",
		"run_dir", runDir, "socket", short)
	return short, nil
}

// Server is a resident MCP HTTP endpoint bound to an AskUserHub.
type Server struct {
	hub        *core.AskUserHub
	http       *http.Server
	baseURL    string
	socketPath string
	mu         sync.Mutex
}

// Start listens on 127.0.0.1:0 and serves /mcp (tests only).
func Start(hub *core.AskUserHub) (*Server, error) {
	return StartOn(hub, defaultListen)
}

// StartUnix listens on the resolved Unix socket path and serves /mcp.
// When configuredSocket is empty, the default is <runDir>/askuser-mcp.sock.
func StartUnix(hub *core.AskUserHub, runDir, configuredSocket string) (*Server, error) {
	if runDir == "" {
		return nil, fmt.Errorf("askuser: run dir required")
	}
	sockPath, err := ResolveSocketPath(runDir, configuredSocket)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		return nil, fmt.Errorf("askuser: create socket dir: %w", err)
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("askuser: listen unix: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("askuser: chmod socket: %w", err)
	}
	return serve(hub, ln, unixMCPHost, sockPath)
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
	return serve(hub, ln, "http://"+ln.Addr().String(), "")
}

func serve(hub *core.AskUserHub, ln net.Listener, baseURL, socketPath string) (*Server, error) {
	s := &Server{hub: hub, baseURL: baseURL, socketPath: socketPath}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.http = &http.Server{Handler: mux}
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("askuser mcp: serve error", "error", err)
		}
	}()
	if socketPath != "" {
		slog.Info("askuser mcp: listening", "socket", socketPath, "url", s.MCPURL())
	} else {
		slog.Info("askuser mcp: listening", "url", s.MCPURL())
	}
	return s, nil
}

// MCPURL is the Streamable HTTP endpoint path used by local bridge clients.
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

// SocketPath returns the Unix socket path when listening on unix; empty for TCP.
func (s *Server) SocketPath() string {
	if s == nil {
		return ""
	}
	return s.socketPath
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
		result = map[string]any{"tools": []any{toolDescriptor(), clientFlowToolDescriptor()}}
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
					"description": "发送事件（可选）。" +
						"枚举：connect_account（有连接新账户需求时）",
					"enum": []any{
						EventConnectAccount,
						"",
					},
				},
				"allow_custom_input": map[string]any{
					"type":        "boolean",
					"description": "仅当确实接受自由输入时设为 true。",
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

func clientFlowToolDescriptor() map[string]any {
	return map[string]any{
		"name": core.ToolCCConnectClientFlow,
		"description": "非阻塞地引导 App 打开自有业务流程（如绑定账户、创建任务、去任务中心审批）。" +
			"不用于普通确认问答，也不等待用户 respond。",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"type", "description"},
			"properties": map[string]any{
				"type": map[string]any{
					"type": "string",
					"description": "发送事件（可选）。" +
						"枚举：connect_account（有连接新账户需求时）、create_task（任务保存的时候）、" +
						"task_generating（触发子Agent生成任务时）、task_center_approval（任务修改/保存后需要去任务中心审批）、" +
						"credits_insufficient（LLM 额度不足时引导充值）。",
					"enum": []any{
						EventConnectAccount,
						EventCreateTask,
						EventTaskGenerating,
						EventTaskCenterApproval,
						EventCreditsInsufficient,
					},
				},
				"description": map[string]any{
					"type":        "string",
					"description": "流程说明文案（必填），展示给用户引导其进入 App 自有流程。",
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
	if sessionKey == "" {
		return nil, fmt.Errorf("missing %s header", core.SessionKeyHeader)
	}
	switch params.Name {
	case toolName, core.MCPQualifiedAskUserTool:
		return s.callAsk(ctx, sessionKey, params.Arguments)
	case core.ToolCCConnectClientFlow, core.MCPQualifiedClientFlowTool:
		return s.callClientFlow(sessionKey, params.Arguments)
	default:
		return nil, fmt.Errorf("unknown tool %q", params.Name)
	}
}

func (s *Server) callAsk(ctx context.Context, sessionKey string, argsRaw json.RawMessage) (any, error) {
	q, err := ParseToolArguments(argsRaw)
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

func (s *Server) callClientFlow(sessionKey string, argsRaw json.RawMessage) (any, error) {
	in, err := ParseClientFlowArguments(argsRaw)
	if err != nil {
		return nil, err
	}
	if err := s.hub.EmitClientFlow(sessionKey, in.Type, in.Description); err != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "client_flow failed: " + err.Error()}},
			"isError": true,
		}, nil
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("client_flow emitted: %s", in.Type)}},
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

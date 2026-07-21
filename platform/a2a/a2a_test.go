package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/chenhg5/cc-connect/core"
)

func completeA2ATask(t *testing.T, platform core.Platform, replyCtx any) {
	t.Helper()
	scp, ok := platform.(core.StreamingCardPlatform)
	if !ok {
		t.Fatal("platform does not implement StreamingCardPlatform")
	}
	card, err := scp.CreateStreamingCard(context.Background(), replyCtx)
	if err != nil {
		t.Fatalf("CreateStreamingCard() error = %v", err)
	}
	if err := card.Finalize(context.Background(), ""); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
}

func TestNewDefaults(t *testing.T) {
	plat, err := New(map[string]any{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	if p.listenAddr != ":8010" {
		t.Fatalf("listenAddr = %q, want :8010", p.listenAddr)
	}
	if p.path != "/a2a/" {
		t.Fatalf("path = %q, want /a2a/", p.path)
	}
	if p.timeout != 30*time.Minute {
		t.Fatalf("timeout = %s, want 30m", p.timeout)
	}
	if p.taskTTL != 2*time.Hour {
		t.Fatalf("taskTTL = %s, want 2h", p.taskTTL)
	}
	if p.maxTasks != 1000 {
		t.Fatalf("maxTasks = %d, want 1000", p.maxTasks)
	}
	if p.agentVersion != "dev" {
		t.Fatalf("agentVersion = %q, want dev", p.agentVersion)
	}
	if p.userHeader != "X-A2A-User" {
		t.Fatalf("userHeader = %q, want X-A2A-User", p.userHeader)
	}
}

func TestNewCustomOptions(t *testing.T) {
	plat, err := New(map[string]any{
		"listen":            "127.0.0.1:0",
		"path":              "a2a",
		"public_url":        "https://agent.example.com/",
		"token":             "secret",
		"agent_name":        "Custom Agent",
		"agent_description": "Custom description",
		"agent_version":     "v10.0.0",
		"request_timeout":   "5m",
		"task_ttl":          "1h",
		"max_tasks":         int64(42),
		"allow_from":        "alice",
		"user_header":       "x-caller-user",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	if p.listenAddr != "127.0.0.1:0" {
		t.Fatalf("listenAddr = %q", p.listenAddr)
	}
	if p.path != "/a2a/" {
		t.Fatalf("path = %q, want /a2a/", p.path)
	}
	if p.publicURL != "https://agent.example.com" {
		t.Fatalf("publicURL = %q", p.publicURL)
	}
	if p.apiToken != "secret" || p.agentName != "Custom Agent" || p.description != "Custom description" || p.agentVersion != "v10.0.0" {
		t.Fatalf("unexpected platform metadata: %+v", p)
	}
	if p.timeout != 5*time.Minute || p.taskTTL != time.Hour || p.maxTasks != 42 {
		t.Fatalf("unexpected config: timeout=%s taskTTL=%s maxTasks=%d", p.timeout, p.taskTTL, p.maxTasks)
	}
	if p.userHeader != "X-Caller-User" {
		t.Fatalf("userHeader = %q, want X-Caller-User", p.userHeader)
	}
}

func TestEndpointURLWithHostListenAddr(t *testing.T) {
	plat, err := New(map[string]any{
		"listen_addr": "127.0.0.1:8010",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	if got := p.endpointURL(); got != "http://127.0.0.1:8010/a2a/" {
		t.Fatalf("endpointURL() = %q, want http://127.0.0.1:8010/a2a/", got)
	}
}

func TestNewRejectsInvalidPath(t *testing.T) {
	if _, err := New(map[string]any{"path": "://bad"}); err == nil {
		t.Fatal("expected invalid path error")
	}
}

func TestPlatformName(t *testing.T) {
	plat, err := New(map[string]any{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := plat.Name(); got != "a2a" {
		t.Fatalf("Name() = %q, want a2a", got)
	}
}

func TestSDKAgentCardEndpoint(t *testing.T) {
	plat, err := New(map[string]any{
		"path":              "/a2a/",
		"public_url":        "https://agent.example.com/",
		"agent_name":        "Custom Agent",
		"agent_description": "Custom description",
		"agent_version":     "v10.0.0",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	req := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var card map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	if card["name"] != "Custom Agent" {
		t.Fatalf("name = %v", card["name"])
	}
	if card["description"] != "Custom description" {
		t.Fatalf("description = %v", card["description"])
	}
	if card["version"] != "v10.0.0" {
		t.Fatalf("version = %v", card["version"])
	}
	caps, ok := card["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing or wrong type: %T", card["capabilities"])
	}
	if caps["streaming"] != true {
		t.Fatalf("streaming = %v, want true", caps["streaming"])
	}
	if caps["pushNotifications"] != nil {
		t.Fatalf("pushNotifications should be omitted when false, got %v", caps["pushNotifications"])
	}

	interfaces, ok := card["supportedInterfaces"].([]any)
	if !ok || len(interfaces) != 1 {
		t.Fatalf("supportedInterfaces = %#v", card["supportedInterfaces"])
	}
	iface := interfaces[0].(map[string]any)
	if iface["url"] != "https://agent.example.com/a2a/" {
		t.Fatalf("interface url = %v", iface["url"])
	}
	if iface["protocolBinding"] != string(a2a.TransportProtocolJSONRPC) {
		t.Fatalf("protocolBinding = %v", iface["protocolBinding"])
	}
}

func TestSDKAgentCardEndpointUsesCustomSkills(t *testing.T) {
	plat, err := New(map[string]any{
		"path": "/a2a/",
		"skills": []any{
			map[string]any{
				"id":          "code-review",
				"name":        "Code Review",
				"description": "Review code changes and suggest fixes.",
				"tags":        []any{"code", "review"},
				"examples":    []any{"Review this pull request"},
				"input_modes": []any{"text/plain"},
				"output_modes": []any{
					"text/plain",
					"application/json",
				},
			},
			map[string]any{
				"id":          "coding",
				"name":        "Coding Agent",
				"description": "Implement and test code changes.",
				"tags":        []string{"coding", "automation"},
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	req := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var card map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	skills, ok := card["skills"].([]any)
	if !ok || len(skills) != 2 {
		t.Fatalf("skills = %#v, want two custom skills", card["skills"])
	}
	first := skills[0].(map[string]any)
	if first["id"] != "code-review" || first["name"] != "Code Review" || first["description"] != "Review code changes and suggest fixes." {
		t.Fatalf("first skill = %#v", first)
	}
	if got := first["tags"].([]any); len(got) != 2 || got[0] != "code" || got[1] != "review" {
		t.Fatalf("first tags = %#v", first["tags"])
	}
	if got := first["examples"].([]any); len(got) != 1 || got[0] != "Review this pull request" {
		t.Fatalf("first examples = %#v", first["examples"])
	}
	if got := first["inputModes"].([]any); len(got) != 1 || got[0] != "text/plain" {
		t.Fatalf("first inputModes = %#v", first["inputModes"])
	}
	if got := first["outputModes"].([]any); len(got) != 2 || got[1] != "application/json" {
		t.Fatalf("first outputModes = %#v", first["outputModes"])
	}
}

func TestNewRejectsInvalidCustomSkill(t *testing.T) {
	_, err := New(map[string]any{
		"skills": []any{
			map[string]any{
				"id":   "missing-description",
				"name": "Missing Description",
			},
		},
	})
	if err == nil {
		t.Fatal("New() error = nil, want invalid skill error")
	}
	if !strings.Contains(err.Error(), "skills[0]") {
		t.Fatalf("New() error = %v, want skills[0] context", err)
	}
}

func TestSDKAgentCardEndpointDerivesURLFromForwardedHeaders(t *testing.T) {
	plat, err := New(map[string]any{"path": "/a2a/"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	req := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "proxy.example.com")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var card map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	interfaces := card["supportedInterfaces"].([]any)
	iface := interfaces[0].(map[string]any)
	if iface["url"] != "https://proxy.example.com/a2a/" {
		t.Fatalf("interface url = %v, want https://proxy.example.com/a2a/", iface["url"])
	}
}

func TestSDKAgentCardEndpointDerivesURLFromForwardedHeader(t *testing.T) {
	plat, err := New(map[string]any{"path": "/a2a/"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	req := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	req.Header.Set("Forwarded", `for=192.0.2.1;proto=https;host="forwarded.example.com"`)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var card map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	interfaces := card["supportedInterfaces"].([]any)
	iface := interfaces[0].(map[string]any)
	if iface["url"] != "https://forwarded.example.com/a2a/" {
		t.Fatalf("interface url = %v, want https://forwarded.example.com/a2a/", iface["url"])
	}
}

func TestSDKAgentCardEndpointPublicURLOverridesForwardedHeaders(t *testing.T) {
	plat, err := New(map[string]any{
		"path":       "/a2a/",
		"public_url": "https://configured.example.com",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	req := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "proxy.example.com")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := agentCardURL(t, rec.Body.Bytes()); got != "https://configured.example.com/a2a/" {
		t.Fatalf("agent card url = %q, want configured public_url", got)
	}
}

func TestSDKAgentCardEndpointFallsBackToHostHeader(t *testing.T) {
	plat, err := New(map[string]any{"path": "/a2a/"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	req := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	req.Host = "host.example.com"
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := agentCardURL(t, rec.Body.Bytes()); got != "http://host.example.com/a2a/" {
		t.Fatalf("agent card url = %q, want host fallback", got)
	}
}

func TestSDKAgentCardEndpointUsesFirstForwardedHeaderValue(t *testing.T) {
	plat, err := New(map[string]any{"path": "/a2a/"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)

	req := httptest.NewRequest(http.MethodGet, "/a2a/.well-known/agent-card.json", nil)
	req.Header.Set("X-Forwarded-Proto", "https,http")
	req.Header.Set("X-Forwarded-Host", "first.example.com,second.example.com")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := agentCardURL(t, rec.Body.Bytes()); got != "https://first.example.com/a2a/" {
		t.Fatalf("agent card url = %q, want first forwarded value", got)
	}
}

func TestProcessingEndIgnoresAlreadyFinishedTask(t *testing.T) {
	p := newTestPlatform(t, map[string]any{})
	waiter, ok := p.pending.create("task-1")
	if !ok {
		t.Fatal("create pending task failed")
	}
	if !p.finishTask("task-1", pendingResult{state: a2a.TaskStateCompleted}) {
		t.Fatal("finishTask returned false")
	}
	if err := p.OnProcessingEnd(context.Background(), replyContext{taskID: "task-1"}, core.ProcessingEndEvent{Kind: core.ProcessingEndCommand}); err != nil {
		t.Fatalf("OnProcessingEnd() error = %v", err)
	}
	select {
	case <-waiter.done:
	default:
		t.Fatal("done channel was not closed")
	}
}

func TestJSONRPCSlashCommandAutoCompletes(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if err := platform.Reply(context.Background(), msg.ReplyCtx, "sessions: 3 active"); err != nil {
			t.Errorf("Reply() error = %v", err)
		}
		notifier, ok := platform.(core.ProcessingEndNotifier)
		if !ok {
			t.Fatal("platform does not implement ProcessingEndNotifier")
		}
		if err := notifier.OnProcessingEnd(context.Background(), msg.ReplyCtx, core.ProcessingEndEvent{Kind: core.ProcessingEndCommand}); err != nil {
			t.Errorf("OnProcessingEnd() error = %v", err)
		}
	})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("/list")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage() result = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("task state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].Parts[0].Text() != "sessions: 3 active" {
		t.Fatalf("artifacts = %#v", task.Artifacts)
	}
}

func TestJSONRPCSendMessageStreamsMultipleArtifacts(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		ctx := context.Background()
		if err := platform.Reply(ctx, msg.ReplyCtx, "part-1"); err != nil {
			t.Errorf("first Reply() error = %v", err)
		}
		if err := platform.Reply(ctx, msg.ReplyCtx, "part-2"); err != nil {
			t.Errorf("second Reply() error = %v", err)
		}
		completeA2ATask(t, platform, msg.ReplyCtx)
	})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage() result = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("task state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) != 2 {
		t.Fatalf("artifact count = %d, want 2", len(task.Artifacts))
	}
	if task.Artifacts[0].Parts[0].Text() != "part-1" || task.Artifacts[1].Parts[0].Text() != "part-2" {
		t.Fatalf("artifacts = %#v, want part-1 and part-2", task.Artifacts)
	}
}

func TestJSONRPCStreamingCardUpdatesReplaceOneArtifact(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		scp, ok := platform.(core.StreamingCardPlatform)
		if !ok {
			t.Fatal("platform does not implement StreamingCardPlatform")
		}
		card, err := scp.CreateStreamingCard(context.Background(), msg.ReplyCtx)
		if err != nil {
			t.Fatalf("CreateStreamingCard() error = %v", err)
		}
		if err := card.Update(context.Background(), "draft"); err != nil {
			t.Errorf("first Update() error = %v", err)
		}
		if err := card.Update(context.Background(), "almost final"); err != nil {
			t.Errorf("second Update() error = %v", err)
		}
		if err := card.Finalize(context.Background(), "final"); err != nil {
			t.Errorf("Finalize() error = %v", err)
		}
	})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage() result = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("task state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(task.Artifacts))
	}
	if got := task.Artifacts[0].Parts[0].Text(); got != "final" {
		t.Fatalf("artifact text = %q, want final", got)
	}
}

func TestJSONRPCSendMessageCompletesTaskWithArtifact(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s", "api_token": "secret"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if msg.Platform != "a2a" {
			t.Errorf("Platform = %q, want a2a", msg.Platform)
		}
		if msg.SessionKey != "a2a:ctx-1" {
			t.Errorf("SessionKey = %q, want a2a:ctx-1", msg.SessionKey)
		}
		if msg.UserID != "alice" {
			t.Errorf("UserID = %q, want alice", msg.UserID)
		}
		ctx := context.Background()
		if err := platform.Reply(ctx, msg.ReplyCtx, "done"); err != nil {
			t.Errorf("Reply() error = %v", err)
		}
		completeA2ATask(t, platform, msg.ReplyCtx)
	})

	client := newA2AClient(t, server.URL+"/a2a/", headerInterceptor{
		"Authorization": {"Bearer secret"},
		"X-A2A-User":    {"alice"},
	})
	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:        "msg-1",
			ContextID: "ctx-1",
			Role:      a2a.MessageRoleUser,
			Parts:     a2a.ContentParts{a2a.NewTextPart("hello")},
		},
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage() result = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("task state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) != 1 || len(task.Artifacts[0].Parts) != 1 || task.Artifacts[0].Parts[0].Text() != "done" {
		t.Fatalf("task artifacts = %#v, want text artifact done", task.Artifacts)
	}
}

func TestJSONRPCSendMessageUsesConfiguredUserHeader(t *testing.T) {
	p := newTestPlatform(t, map[string]any{
		"path":        "/a2a/",
		"timeout":     "2s",
		"api_token":   "secret",
		"user_header": "X-Caller-User",
	})
	server := httptest.NewServer(p.routes())
	defer server.Close()

	received := make(chan *core.Message, 1)
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		received <- msg
		if err := platform.Reply(context.Background(), msg.ReplyCtx, "done"); err != nil {
			t.Errorf("Reply() error = %v", err)
		}
		completeA2ATask(t, platform, msg.ReplyCtx)
	})

	client := newA2AClient(t, server.URL+"/a2a/", headerInterceptor{
		"Authorization":   {"Bearer secret"},
		"X-Caller-User":   {"carol"},
		"X-A2A-User":      {"alice"},
		"X-Unused-Header": {"bob"},
	})
	if _, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:        "msg-1",
			ContextID: "ctx-1",
			Role:      a2a.MessageRoleUser,
			Parts:     a2a.ContentParts{a2a.NewTextPart("hello")},
		},
	}); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	select {
	case msg := <-received:
		if msg.UserID != "carol" {
			t.Fatalf("UserID = %q, want carol", msg.UserID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler")
	}
}

func TestJSONRPCCancelTaskCancelsInFlightSend(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	received := make(chan replyContext, 1)
	p.setHandler(func(_ core.Platform, msg *core.Message) {
		received <- msg.ReplyCtx.(replyContext)
	})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	sendDone := make(chan a2a.SendMessageResult, 1)
	sendErr := make(chan error, 1)
	go func() {
		result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
			Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("please wait")),
		})
		if err != nil {
			sendErr <- err
			return
		}
		sendDone <- result
	}()

	var rc replyContext
	select {
	case rc = <-received:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler")
	}
	waitForTask(t, client, a2a.TaskID(rc.taskID))
	canceledTask, err := client.CancelTask(context.Background(), &a2a.CancelTaskRequest{ID: a2a.TaskID(rc.taskID)})
	if err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	if canceledTask.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("CancelTask state = %s, want canceled", canceledTask.Status.State)
	}

	select {
	case err := <-sendErr:
		t.Fatalf("SendMessage() error = %v", err)
	case result := <-sendDone:
		task, ok := result.(*a2a.Task)
		if !ok {
			t.Fatalf("SendMessage result = %T, want *a2a.Task", result)
		}
		if task.Status.State != a2a.TaskStateCanceled {
			t.Fatalf("SendMessage final state = %s, want canceled", task.Status.State)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SendMessage cancellation result")
	}
}

func TestJSONRPCGetTaskAfterSendMessage(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		if err := platform.Reply(context.Background(), msg.ReplyCtx, "done"); err != nil {
			t.Errorf("Reply() error = %v", err)
		}
		completeA2ATask(t, platform, msg.ReplyCtx)
	})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	sentTask := result.(*a2a.Task)
	got, err := client.GetTask(context.Background(), &a2a.GetTaskRequest{ID: sentTask.ID})
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.ID != sentTask.ID || got.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("GetTask() = id %q state %s, want id %q completed", got.ID, got.Status.State, sentTask.ID)
	}
}

func TestJSONRPCTimeoutReturnsFailedTask(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "1ms"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	p.setHandler(func(_ core.Platform, _ *core.Message) {})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("never reply")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage result = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateFailed {
		t.Fatalf("task state = %s, want failed", task.Status.State)
	}
}

func TestJSONRPCRejectsWhenPendingCapacityFull(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s", "max_tasks": 1})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	received := make(chan replyContext, 2)
	p.setHandler(func(_ core.Platform, msg *core.Message) {
		received <- msg.ReplyCtx.(replyContext)
	})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	firstDone := make(chan a2a.SendMessageResult, 1)
	firstErr := make(chan error, 1)
	go func() {
		result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
			Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hold slot")),
		})
		if err != nil {
			firstErr <- err
			return
		}
		firstDone <- result
	}()
	var firstRC replyContext
	select {
	case firstRC = <-received:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first handler")
	}

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("over capacity")),
	})
	if err != nil {
		t.Fatalf("second SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("second SendMessage result = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateRejected {
		t.Fatalf("second task state = %s, want rejected", task.Status.State)
	}

	p.pending.cancel(firstRC.taskID)
	select {
	case err := <-firstErr:
		t.Fatalf("first SendMessage() error = %v", err)
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first SendMessage cleanup")
	}
}

func TestJSONRPCRejectsMissingBearer(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "api_token": "secret"})
	server := httptest.NewServer(p.routes())
	defer server.Close()

	body := []byte(`{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"msg-1","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`)
	req := httptest.NewRequest(http.MethodPost, server.URL+"/a2a/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want JSON-RPC HTTP 200 body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["error"] == nil {
		t.Fatalf("response = %s, want JSON-RPC error", rec.Body.String())
	}
}

func TestPartsToCoreMapsDataRawAndURLParts(t *testing.T) {
	content, images, audio, files, err := partsToCore(a2a.ContentParts{
		a2a.NewTextPart("hello"),
		a2a.NewDataPart(map[string]any{"ok": true}),
		a2a.NewRawPart([]byte("file-bytes")),
		a2a.NewFileURLPart(a2a.URL("https://example.com/file.txt"), "text/plain"),
	})
	if err != nil {
		t.Fatalf("partsToCore() error = %v", err)
	}
	wantContent := "hello\n\n{\"ok\":true}\n\nFile URL: https://example.com/file.txt"
	if content != wantContent {
		t.Fatalf("content = %q, want %q", content, wantContent)
	}
	if len(images) != 0 || audio != nil {
		t.Fatalf("images = %#v audio = %#v, want none", images, audio)
	}
	if len(files) != 1 || string(files[0].Data) != "file-bytes" {
		t.Fatalf("files = %#v, want one raw file", files)
	}
}

func TestPartsToCoreClassifiesImageAudioAndFile(t *testing.T) {
	imagePart := a2a.NewRawPart([]byte{0x89, 'P', 'N', 'G', 1, 2, 3})
	imagePart.MediaType = "image/png"
	imagePart.Filename = "photo.png"

	audioPart := a2a.NewRawPart([]byte("audio-data"))
	audioPart.MediaType = "audio/ogg"
	audioPart.Filename = "voice.ogg"

	filePart := a2a.NewRawPart([]byte("pdf-data"))
	filePart.MediaType = "application/pdf"
	filePart.Filename = "report.pdf"

	content, images, audio, files, err := partsToCore(a2a.ContentParts{imagePart, audioPart, filePart})
	if err != nil {
		t.Fatalf("partsToCore() error = %v", err)
	}
	if content != "" {
		t.Fatalf("content = %q, want empty", content)
	}
	if len(images) != 1 || images[0].FileName != "photo.png" || string(images[0].Data) != string(imagePart.Raw()) {
		t.Fatalf("images = %#v", images)
	}
	if audio == nil || audio.Format != "ogg" || string(audio.Data) != "audio-data" {
		t.Fatalf("audio = %#v", audio)
	}
	if len(files) != 1 || files[0].FileName != "report.pdf" || string(files[0].Data) != "pdf-data" {
		t.Fatalf("files = %#v", files)
	}
}

func TestPartsToCoreDownloadsURLPart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("pdf-bytes"))
	}))
	defer server.Close()

	content, images, audio, files, err := partsToCore(a2a.ContentParts{
		a2a.NewFileURLPart(a2a.URL(server.URL+"/report.pdf"), ""),
	})
	if err != nil {
		t.Fatalf("partsToCore() error = %v", err)
	}
	if content != "" || len(images) != 0 || audio != nil {
		t.Fatalf("content/images/audio = %q %#v %#v, want empty", content, images, audio)
	}
	if len(files) != 1 || files[0].MimeType != "application/pdf" || string(files[0].Data) != "pdf-bytes" {
		t.Fatalf("files = %#v", files)
	}
}

func TestSendFileEmitsRawArtifact(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-1")
	if !ok {
		t.Fatal("create returned false")
	}
	if err := p.SendFile(context.Background(), replyContext{taskID: "task-1"}, core.FileAttachment{
		FileName: "report.pdf",
		MimeType: "application/pdf",
		Data:     []byte("pdf-data"),
	}); err != nil {
		t.Fatalf("SendFile() error = %v", err)
	}
	select {
	case artifact := <-waiter.artifacts:
		if len(artifact.parts) != 1 {
			t.Fatalf("parts = %#v, want one raw part", artifact.parts)
		}
		if got := string(artifact.parts[0].Raw()); got != "pdf-data" {
			t.Fatalf("raw = %q, want pdf-data", got)
		}
		if artifact.parts[0].Filename != "report.pdf" || artifact.parts[0].MediaType != "application/pdf" {
			t.Fatalf("part = %#v", artifact.parts[0])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for artifact")
	}
	select {
	case <-waiter.done:
		t.Fatal("task should not be completed yet")
	default:
	}
}

func TestSendImageEmitsRawArtifact(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-1")
	if !ok {
		t.Fatal("create returned false")
	}
	if err := p.SendImage(context.Background(), replyContext{taskID: "task-1"}, core.ImageAttachment{
		FileName: "photo.png",
		MimeType: "image/png",
		Data:     []byte{0x89, 'P', 'N', 'G'},
	}); err != nil {
		t.Fatalf("SendImage() error = %v", err)
	}
	select {
	case artifact := <-waiter.artifacts:
		if len(artifact.parts) != 1 || artifact.parts[0].MediaType != "image/png" {
			t.Fatalf("parts = %#v", artifact.parts)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for artifact")
	}
}

func TestJSONRPCOutboundFileRoundtrip(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"path": "/a2a/", "timeout": "2s"})
	server := httptest.NewServer(p.routes())
	defer server.Close()
	p.setHandler(func(platform core.Platform, msg *core.Message) {
		fileSender, ok := platform.(core.FileSender)
		if !ok {
			t.Fatal("platform does not implement FileSender")
		}
		if err := fileSender.SendFile(context.Background(), msg.ReplyCtx, core.FileAttachment{
			FileName: "report.pdf",
			MimeType: "application/pdf",
			Data:     []byte("pdf-data"),
		}); err != nil {
			t.Errorf("SendFile() error = %v", err)
		}
		streaming, ok := platform.(core.StreamingCardPlatform)
		if !ok {
			t.Fatal("platform does not implement StreamingCardPlatform")
		}
		if card, err := streaming.CreateStreamingCard(context.Background(), msg.ReplyCtx); err != nil {
			t.Errorf("CreateStreamingCard() error = %v", err)
		} else if err := card.Finalize(context.Background(), ""); err != nil {
			t.Errorf("Finalize() error = %v", err)
		}
	})
	client := newA2AClient(t, server.URL+"/a2a/", nil)

	result, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("send file")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage() result = %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("task state = %s, want completed", task.Status.State)
	}
	if len(task.Artifacts) != 1 || len(task.Artifacts[0].Parts) != 1 {
		t.Fatalf("artifacts = %#v", task.Artifacts)
	}
	part := task.Artifacts[0].Parts[0]
	if got := string(part.Raw()); got != "pdf-data" {
		t.Fatalf("raw = %q, want pdf-data", got)
	}
	if part.Filename != "report.pdf" || part.MediaType != "application/pdf" {
		t.Fatalf("part = %#v", part)
	}
}

func TestFinishTaskCompletesOnce(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-1")
	if !ok {
		t.Fatal("create returned false")
	}

	if !p.finishTask("task-1", pendingResult{state: a2a.TaskStateCompleted}) {
		t.Fatal("finishTask returned false")
	}
	if p.finishTask("task-1", pendingResult{state: a2a.TaskStateCompleted}) {
		t.Fatal("second finishTask returned true")
	}

	select {
	case result := <-waiter.done:
		if result.state != a2a.TaskStateCompleted {
			t.Fatalf("state = %s, want completed", result.state)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pending result")
	}
}

func TestPushArtifactDoesNotCompleteTask(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-1")
	if !ok {
		t.Fatal("create returned false")
	}
	if !p.pushArtifact("task-1", "hello") {
		t.Fatal("pushArtifact returned false")
	}
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "hello" || artifact.artifactID != "" {
			t.Fatalf("artifact = %+v, want new text artifact hello", artifact)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for artifact")
	}
	select {
	case <-waiter.done:
		t.Fatal("task should not be completed yet")
	default:
	}
}

func TestPendingStoreRejectsWhenFull(t *testing.T) {
	store := newPendingStore(1, time.Hour)
	if _, ok := store.create("task-1"); !ok {
		t.Fatal("first create returned false")
	}
	if task, ok := store.create("task-2"); ok || task != nil {
		t.Fatalf("second create = (%+v, %v), want nil false", task, ok)
	}
}

func TestPendingStoreRejectsDuplicateTaskID(t *testing.T) {
	store := newPendingStore(10, time.Hour)
	first, ok := store.create("task-1")
	if !ok {
		t.Fatal("first create returned false")
	}
	second, ok := store.create("task-1")
	if ok || second != nil {
		t.Fatalf("duplicate create = (%+v, %v), want nil false", second, ok)
	}
	if got, ok := store.get("task-1"); !ok || got != first {
		t.Fatalf("stored task = (%+v, %v), want original task", got, ok)
	}
}

func TestPendingStoreCleanupExpired(t *testing.T) {
	store := newPendingStore(10, time.Nanosecond)
	if _, ok := store.create("task-1"); !ok {
		t.Fatal("create returned false")
	}
	time.Sleep(time.Millisecond)
	if _, ok := store.create("task-2"); !ok {
		t.Fatal("create after ttl cleanup returned false")
	}
	if _, ok := store.get("task-1"); ok {
		t.Fatal("expired pending task still exists")
	}
}

func TestStreamingCardUpdateStreamsArtifactFinalizeCompletes(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-1")
	if !ok {
		t.Fatal("create returned false")
	}
	ctx := context.Background()
	card, err := p.CreateStreamingCard(ctx, replyContext{taskID: "task-1"})
	if err != nil {
		t.Fatalf("CreateStreamingCard() error = %v", err)
	}
	if err := card.Update(ctx, "working"); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "working" || artifact.artifactID == "" {
			t.Fatalf("artifact = %+v, want update artifact working", artifact)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for update artifact")
	}
	select {
	case <-waiter.done:
		t.Fatal("Update should not complete task")
	default:
	}
	if err := card.Finalize(ctx, "final"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "final" || artifact.artifactID == "" || !artifact.lastChunk {
			t.Fatalf("artifact = %+v, want final last update artifact", artifact)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for finalize artifact")
	}
	select {
	case result := <-waiter.done:
		if result.state != a2a.TaskStateCompleted {
			t.Fatalf("state = %s, want completed", result.state)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for final result")
	}
}

func TestStreamingCardStructuredEventsPushAnswerNotMarkdown(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-struct")
	if !ok {
		t.Fatal("create returned false")
	}
	ctx := context.Background()
	card, err := p.CreateStreamingCard(ctx, replyContext{taskID: "task-struct"})
	if err != nil {
		t.Fatalf("CreateStreamingCard() error = %v", err)
	}
	ssc, ok := card.(core.StructuredStreamingCard)
	if !ok {
		t.Fatal("streamingCard must implement StructuredStreamingCard")
	}

	_ = ssc.OnTurnStreamEvent(ctx, core.TurnStreamEvent{
		Kind: core.TurnStreamThinkingReplace, Thinking: "planning",
	})
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "planning" {
			t.Fatalf("thinking artifact = %q", artifact.content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for thinking artifact")
	}

	_ = ssc.OnTurnStreamEvent(ctx, core.TurnStreamEvent{
		Kind: core.TurnStreamAnswerAppend, Answer: "Hel",
	})
	_ = ssc.OnTurnStreamEvent(ctx, core.TurnStreamEvent{
		Kind: core.TurnStreamAnswerAppend, Answer: "lo",
	})
	// Dual-write markdown must be ignored once structured.
	badCard := "💭 **Thinking**\n\nplanning\n\n---\n\n---\n\nHello\n\n---\n\ntruncated"
	if err := card.Update(ctx, badCard); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "Hel" {
			t.Fatalf("first answer artifact = %q, want Hel", artifact.content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Hel")
	}
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "Hello" {
			t.Fatalf("second answer artifact = %q, want Hello", artifact.content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Hello")
	}
	select {
	case artifact := <-waiter.artifacts:
		t.Fatalf("unexpected artifact after ignored Update: %+v", artifact)
	default:
	}

	if err := card.Finalize(ctx, badCard); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "Hello" || !artifact.lastChunk {
			t.Fatalf("final artifact = %+v, want Hello lastChunk", artifact)
		}
		if strings.Contains(artifact.content, "Thinking") || strings.Contains(artifact.content, "---") {
			t.Fatalf("markdown leaked into artifact: %q", artifact.content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for finalize artifact")
	}
	select {
	case result := <-waiter.done:
		if result.state != a2a.TaskStateCompleted {
			t.Fatalf("state = %s", result.state)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for done")
	}
}

func TestStreamingCardStructuredIgnoresToolEventsForArtifact(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-tools")
	if !ok {
		t.Fatal("create returned false")
	}
	ctx := context.Background()
	card, _ := p.CreateStreamingCard(ctx, replyContext{taskID: "task-tools"})
	ssc := card.(core.StructuredStreamingCard)
	_ = ssc.OnTurnStreamEvent(ctx, core.TurnStreamEvent{
		Kind: core.TurnStreamToolUpsert,
		Tool: core.TurnToolCall{Index: 1, Name: "Bash", Input: "date"},
	})
	select {
	case artifact := <-waiter.artifacts:
		t.Fatalf("tool upsert should not push artifact, got %+v", artifact)
	default:
	}
	_ = ssc.OnTurnStreamEvent(ctx, core.TurnStreamEvent{
		Kind: core.TurnStreamAnswerAppend, Answer: "done",
	})
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "done" {
			t.Fatalf("artifact = %q", artifact.content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
	_ = card.Finalize(ctx, "")
	select {
	case artifact := <-waiter.artifacts:
		if artifact.content != "done" || !artifact.lastChunk {
			t.Fatalf("final = %+v", artifact)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out finalize artifact")
	}
	select {
	case <-waiter.done:
	case <-time.After(time.Second):
		t.Fatal("timed out done")
	}
}

func TestToCoreMessageUsesA2AContextAsSessionKey(t *testing.T) {
	p := &Platform{}
	msg, err := p.toCoreMessage(&a2asrv.ExecutorContext{
		TaskID:    a2a.TaskID("task-1"),
		ContextID: "ctx-1",
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("toCoreMessage() error = %v", err)
	}
	if msg.SessionKey != "a2a:ctx-1" {
		t.Fatalf("SessionKey = %q, want a2a:ctx-1", msg.SessionKey)
	}
	if msg.ChannelKey != "ctx-1" {
		t.Fatalf("ChannelKey = %q, want ctx-1", msg.ChannelKey)
	}
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok {
		t.Fatalf("ReplyCtx type = %T, want replyContext", msg.ReplyCtx)
	}
	if rc.taskID != "task-1" || rc.sessionKey != "a2a:ctx-1" {
		t.Fatalf("reply context = %+v, want taskID task-1 sessionKey a2a:ctx-1", rc)
	}
}

func TestReplyAndSendRejectInvalidContext(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	if err := p.Reply(context.Background(), "bad", "content"); err == nil {
		t.Fatal("Reply() error = nil, want error")
	}
	if err := p.Send(context.Background(), 123, "content"); err == nil {
		t.Fatal("Send() error = nil, want error")
	}
}

func TestExecuteYieldsCanceledWhenPendingCanceled(t *testing.T) {
	p := &Platform{
		timeout: time.Second,
		pending: newPendingStore(defaultMaxTasks, defaultTaskTTL),
	}
	p.setHandler(func(_ core.Platform, _ *core.Message) {})

	exec := &sdkExecutor{platform: p}
	execCtx := &a2asrv.ExecutorContext{
		TaskID:    a2a.TaskID("task-1"),
		ContextID: "ctx-1",
		Message:   a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
	}
	var states []a2a.TaskState
	exec.Execute(context.Background(), execCtx)(func(event a2a.Event, err error) bool {
		if err != nil {
			t.Fatalf("event error = %v", err)
		}
		if status, ok := event.(*a2a.TaskStatusUpdateEvent); ok {
			states = append(states, status.Status.State)
			if status.Status.State == a2a.TaskStateWorking {
				p.pending.cancel("task-1")
			}
		}
		return true
	})

	if len(states) == 0 || states[len(states)-1] != a2a.TaskStateCanceled {
		t.Fatalf("states = %#v, want final canceled", states)
	}
}

func TestAuthInterceptorSetsUser(t *testing.T) {
	p := &Platform{apiToken: "secret"}
	interceptor := p.authInterceptor()
	ctx, callCtx := a2asrv.NewCallContext(context.Background(), a2asrv.NewServiceParams(map[string][]string{
		"Authorization": {"Bearer secret"},
		"X-A2A-User":    {"alice"},
	}))

	nextCtx, result, err := interceptor.Before(ctx, callCtx, &a2asrv.Request{})
	if err != nil {
		t.Fatalf("Before() error = %v", err)
	}
	if result != nil {
		t.Fatalf("Before() result = %#v, want nil", result)
	}
	if nextCtx == nil {
		t.Fatal("Before() context is nil")
	}
	if callCtx.User == nil || callCtx.User.Name != "alice" || !callCtx.User.Authenticated {
		t.Fatalf("CallContext user = %+v, want authenticated alice", callCtx.User)
	}
}

func TestAuthInterceptorRejectsBadBearer(t *testing.T) {
	p := &Platform{apiToken: "secret"}
	interceptor := p.authInterceptor()
	ctx, callCtx := a2asrv.NewCallContext(context.Background(), a2asrv.NewServiceParams(map[string][]string{
		"Authorization": {"Bearer wrong"},
	}))

	_, _, err := interceptor.Before(ctx, callCtx, &a2asrv.Request{})
	if err == nil {
		t.Fatal("Before() error = nil, want unauthorized error")
	}
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("Before() error = %v, want errUnauthorized", err)
	}
}

func agentCardURL(t *testing.T, body []byte) string {
	t.Helper()
	var card map[string]any
	if err := json.Unmarshal(body, &card); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	interfaces, ok := card["supportedInterfaces"].([]any)
	if !ok || len(interfaces) != 1 {
		t.Fatalf("supportedInterfaces = %#v", card["supportedInterfaces"])
	}
	iface, ok := interfaces[0].(map[string]any)
	if !ok {
		t.Fatalf("supportedInterfaces[0] = %#v", interfaces[0])
	}
	urlValue, ok := iface["url"].(string)
	if !ok {
		t.Fatalf("interface url = %#v", iface["url"])
	}
	return urlValue
}

func newTestPlatform(t *testing.T, opts map[string]any) *Platform {
	t.Helper()
	plat, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return plat.(*Platform)
}

func newA2AClient(t *testing.T, endpoint string, interceptor a2aclient.CallInterceptor) *a2aclient.Client {
	t.Helper()
	options := []a2aclient.FactoryOption{}
	if interceptor != nil {
		options = append(options, a2aclient.WithCallInterceptors(interceptor))
	}
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{
		a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC),
	}, options...)
	if err != nil {
		t.Fatalf("NewFromEndpoints() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Destroy(); err != nil {
			t.Errorf("client.Destroy() error = %v", err)
		}
	})
	return client
}

func waitForTask(t *testing.T, client *a2aclient.Client, taskID a2a.TaskID) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := client.GetTask(context.Background(), &a2a.GetTaskRequest{ID: taskID}); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for task %q to become queryable: %v", taskID, lastErr)
}

type headerInterceptor map[string][]string

func (h headerInterceptor) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	for key, values := range h {
		req.ServiceParams.Append(key, values...)
	}
	return ctx, nil, nil
}

func (h headerInterceptor) After(context.Context, *a2aclient.Response) error {
	return nil
}

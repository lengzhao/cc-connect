package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/chenhg5/cc-connect/core"
)

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
	if caps["streaming"] != nil {
		t.Fatalf("streaming should be omitted when false, got %v", caps["streaming"])
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

func TestPendingResultCompletesOnce(t *testing.T) {
	p := &Platform{pending: newPendingStore(defaultMaxTasks, defaultTaskTTL)}
	waiter, ok := p.pending.create("task-1")
	if !ok {
		t.Fatal("create returned false")
	}

	if !p.completePending("task-1", "done") {
		t.Fatal("completePending returned false")
	}
	if p.completePending("task-1", "late") {
		t.Fatal("second completePending returned true")
	}

	select {
	case result := <-waiter.done:
		if result.content != "done" {
			t.Fatalf("content = %q, want done", result.content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pending result")
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

func TestStreamingCardFinalizeCompletesPendingTask(t *testing.T) {
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
	case <-waiter.done:
		t.Fatal("Update should not complete task")
	default:
	}
	if err := card.Finalize(ctx, "final"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	select {
	case result := <-waiter.done:
		if result.content != "final" {
			t.Fatalf("content = %q, want final", result.content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for final result")
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

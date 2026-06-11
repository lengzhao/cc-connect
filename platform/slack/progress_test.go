package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
)

func TestParseSlackProgressStyle(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"", "legacy", false},
		{"legacy", "legacy", false},
		{"assistant_status", progressStyleAssistantStatus, false},
		{"stream", progressStyleStream, false},
		{"invalid", "", true},
	}
	for _, tt := range tests {
		opts := map[string]any{}
		if tt.raw != "" {
			opts["progress_style"] = tt.raw
		}
		got, err := parseSlackProgressStyle(opts)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("parseSlackProgressStyle(%q) error = nil, want error", tt.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseSlackProgressStyle(%q) error = %v", tt.raw, err)
		}
		if got != tt.want {
			t.Fatalf("parseSlackProgressStyle(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNew_ProgressStyleWrapsPlatform(t *testing.T) {
	pAny, err := New(map[string]any{
		"bot_token":      "xoxb-test",
		"app_token":      "xapp-test",
		"progress_style": "stream",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	pp, ok := pAny.(*progressPlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *progressPlatform", pAny)
	}
	if pp.ProgressStyle() != "card" {
		t.Fatalf("ProgressStyle() = %q, want card", pp.ProgressStyle())
	}
	if !pp.SupportsProgressCardPayload() {
		t.Fatal("SupportsProgressCardPayload() = false, want true")
	}
}

func TestThreadTargetUsesTimestampFallback(t *testing.T) {
	p := &Platform{}
	channel, threadTS, ok := p.threadTarget(replyContext{
		channel:   "C123",
		messageTS: "111.222",
	})
	if !ok || channel != "C123" || threadTS != "111.222" {
		t.Fatalf("threadTarget() = (%q, %q, %v), want (C123, 111.222, true)", channel, threadTS, ok)
	}
}

func TestProgressEntryToTaskChunk(t *testing.T) {
	chunk := progressEntryToTaskChunk(core.ProgressCardEntry{
		Kind: core.ProgressEntryToolUse,
		Tool: "Bash",
		Text: "grep pattern",
	}, 42)
	if chunk.ID != "p42" {
		t.Fatalf("id = %q, want p42", chunk.ID)
	}
	if chunk.Title != "Bash" {
		t.Fatalf("title = %q, want Bash", chunk.Title)
	}
	if chunk.Status != slack.TaskCardStatusInProgress {
		t.Fatalf("status = %q, want in_progress", chunk.Status)
	}
	if chunk.Details != "grep pattern" {
		t.Fatalf("details = %q, want grep pattern", chunk.Details)
	}
}

func TestStreamDeltaChunksSendsOnlyNewItems(t *testing.T) {
	items := []core.ProgressCardEntry{
		{Kind: core.ProgressEntryToolUse, Tool: "Read"},
	}
	chunks, next := streamDeltaChunks(items, core.ProgressCardStateRunning, false, 0, false)
	if next != 1 || len(chunks) != 2 {
		t.Fatalf("first delta chunks = %d next = %d, want 2 chunks and next 1", len(chunks), next)
	}
	items = append(items, core.ProgressCardEntry{Kind: core.ProgressEntryToolResult, Tool: "Read"})
	chunks, next = streamDeltaChunks(items, core.ProgressCardStateRunning, false, 1, false)
	if next != 2 || len(chunks) != 1 {
		t.Fatalf("second delta chunks = %d next = %d, want 1 chunk and next 2", len(chunks), next)
	}
	if chunks[0].(slack.TaskUpdateChunk).ID != "p2" {
		t.Fatalf("delta chunk id = %q, want p2", chunks[0].(slack.TaskUpdateChunk).ID)
	}
}

func TestSendPreviewStartSetsAssistantStatus(t *testing.T) {
	var gotMethod string
	var gotStatus string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.URL.Path
		_ = r.ParseForm()
		gotStatus = r.PostForm.Get("status")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := &Platform{
		botToken:      "xoxb-test",
		progressStyle: progressStyleAssistantStatus,
		client:        slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
	}
	rc := replyContext{channel: "C1", timestamp: "100.200"}
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "grep"},
	}, false, "CC", core.LangEnglish, core.ProgressCardStateRunning)
	handle, err := p.SendPreviewStart(context.Background(), rc, payload)
	if err != nil {
		t.Fatalf("SendPreviewStart() error = %v", err)
	}
	if handle == nil {
		t.Fatal("handle = nil")
	}
	if !strings.HasSuffix(gotMethod, "/assistant.threads.setStatus") {
		t.Fatalf("method path = %q, want assistant.threads.setStatus", gotMethod)
	}
	if gotStatus != "is running Bash..." {
		t.Fatalf("status = %q", gotStatus)
	}
}

func TestSendPreviewStartStartsStream(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, strings.TrimPrefix(r.URL.Path, "/"))
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.300"}`))
	}))
	defer srv.Close()

	p := &Platform{
		botToken:      "xoxb-test",
		progressStyle: progressStyleStream,
		client:        slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
	}
	rc := replyContext{channel: "C1", timestamp: "100.200"}
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "README.md"},
	}, false, "CC", core.LangEnglish, core.ProgressCardStateRunning)
	handleAny, err := p.SendPreviewStart(context.Background(), rc, payload)
	if err != nil {
		t.Fatalf("SendPreviewStart() error = %v", err)
	}
	handle, ok := handleAny.(*nativeProgressHandle)
	if !ok || handle.stream == nil || handle.stream.streamTS != "200.300" {
		t.Fatalf("handle = %#v, want stream ts 200.300", handleAny)
	}
	if handle.stream.cancelKeep != nil {
		handle.stream.cancelKeep()
	}
	foundStart := false
	for _, m := range methods {
		if m == "chat.startStream" {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatalf("methods = %v, want chat.startStream", methods)
	}
}

func TestUpdateMessageAppendsStreamChunks(t *testing.T) {
	var appendCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat.appendStream") {
			appendCalled = true
		}
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.300"}`))
	}))
	defer srv.Close()

	p := &Platform{
		botToken:      "xoxb-test",
		progressStyle: progressStyleStream,
		client:        slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")),
	}
	handle := &nativeProgressHandle{
		replyCtx: replyContext{channel: "C1", timestamp: "100.200"},
		mode:     progressStyleStream,
		stream:   &streamProgressHandle{channel: "C1", streamTS: "200.300"},
	}
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryToolUse, Tool: "Read", Text: "README.md"},
	}, false, "CC", core.LangEnglish, core.ProgressCardStateRunning)
	if err := p.UpdateMessage(context.Background(), handle, payload); err != nil {
		t.Fatalf("UpdateMessage() error = %v", err)
	}
	if !appendCalled {
		t.Fatal("expected chat.appendStream call")
	}
}

package chatapi

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLogSSELifecycle_FormatsEventAndCommonFields(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	run := &runState{
		id:             "run_1",
		user:           "u1",
		conversationID: "conv_1",
		channelKey:     "ch1",
		created:        time.Now(),
	}
	logSSELifecycle("start", run)
	logSSELifecycle("disconnect", run, "reason", "client_gone")

	out := buf.String()
	if !strings.Contains(out, "level=INFO") {
		t.Fatalf("expected Info level: %s", out)
	}
	if !strings.Contains(out, "chat-api: sse start") {
		t.Fatalf("missing start log: %s", out)
	}
	if !strings.Contains(out, "run_id=run_1") || !strings.Contains(out, "user=u1") ||
		!strings.Contains(out, "conversation_id=conv_1") || !strings.Contains(out, "channel=ch1") {
		t.Fatalf("missing common fields: %s", out)
	}
	if !strings.Contains(out, "chat-api: sse disconnect") || !strings.Contains(out, "reason=client_gone") {
		t.Fatalf("missing disconnect log: %s", out)
	}
}

func TestTerminalName(t *testing.T) {
	cases := []struct {
		name string
		in   pendingResult
		want string
	}{
		{name: "ok", in: pendingResult{}, want: "message_end"},
		{name: "queued", in: pendingResult{queued: true}, want: "message_queued"},
		{name: "err", in: pendingResult{err: context.Canceled}, want: "error"},
		{name: "canceled", in: pendingResult{userCanceled: true}, want: "error"},
		{name: "timeout", in: pendingResult{err: context.DeadlineExceeded}, want: "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalName(tc.in); got != tc.want {
				t.Fatalf("terminalName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTerminalErrorText(t *testing.T) {
	cases := []struct {
		name string
		in   pendingResult
		want string
	}{
		{name: "ok", in: pendingResult{}, want: ""},
		{name: "canceled", in: pendingResult{userCanceled: true}, want: errUserCanceled.Error()},
		{name: "interaction", in: pendingResult{interactionTimedOut: true}, want: errInteractionTimedOut.Error()},
		{name: "queue_full", in: pendingResult{queueFull: true, errMsg: "queue full"}, want: "queue full"},
		{name: "deadline", in: pendingResult{err: context.DeadlineExceeded}, want: context.DeadlineExceeded.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalErrorText(tc.in); got != tc.want {
				t.Fatalf("terminalErrorText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogSSELifecyclePartial(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	logSSELifecyclePartial("resume_miss", "run_x", "u1", "conv_hint", "reason", "run_not_found")
	out := buf.String()
	if !strings.Contains(out, "chat-api: sse resume_miss") {
		t.Fatalf("missing resume_miss log: %s", out)
	}
	if !strings.Contains(out, "run_id=run_x") || !strings.Contains(out, "reason=run_not_found") ||
		!strings.Contains(out, "conversation_id=conv_hint") {
		t.Fatalf("missing fields: %s", out)
	}
}

func TestMessageWriteFailure_DetachesWithoutStart(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := newTestPlatform(t, map[string]any{})
	run := newRunState("run_early", "u1", "ch1", "sk1", "conv_1", "conv_1:0", p, nil, time.Time{})
	if !p.pending.create(run) {
		t.Fatal("create pending run")
	}
	logSSELifecycle("disconnect", run, "reason", "write_error", "error", "broken pipe")
	run.detach()

	if p.pending.get(run.id) == nil {
		t.Fatal("detached run should remain in pending for resume")
	}
	run.mu.Lock()
	detached := run.detached
	run.mu.Unlock()
	if !detached {
		t.Fatal("run should be detached after write failure")
	}
	out := buf.String()
	if strings.Contains(out, "chat-api: sse start") {
		t.Fatalf("write failure must not log start: %s", out)
	}
	if !strings.Contains(out, "chat-api: sse disconnect") || !strings.Contains(out, "reason=write_error") {
		t.Fatalf("expected disconnect log for write failure: %s", out)
	}
}

func TestComplete_LogsEndWhenDetached(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	run := &runState{
		id:             "run_detached",
		user:           "u1",
		conversationID: "conv_1",
		channelKey:     "ch1",
		created:        time.Now(),
		done:           make(chan pendingResult, 1),
		platform:       &Platform{},
	}
	run.detach()
	if !run.complete(pendingResult{answer: "done offline"}) {
		t.Fatal("complete should succeed once")
	}
	out := buf.String()
	if !strings.Contains(out, "chat-api: sse end") || !strings.Contains(out, "terminal=message_end") {
		t.Fatalf("expected end log after detached complete: %s", out)
	}
}

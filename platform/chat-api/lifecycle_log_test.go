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

func TestComplete_LogsDiscardedWhenDetached(t *testing.T) {
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
	// A detached run reaches no client. This used to log "sse end
	// terminal=message_end", indistinguishable from a real delivery, so lost
	// answers were invisible in the logs.
	if !strings.Contains(out, "chat-api: sse discarded") {
		t.Fatalf("expected discarded log after detached complete: %s", out)
	}
	if !strings.Contains(out, "terminal=message_end") {
		t.Fatalf("expected terminal kind in discarded log: %s", out)
	}
	if !strings.Contains(out, "answer_bytes=12") {
		t.Fatalf("expected answer_bytes for the lost answer: %s", out)
	}
	if strings.Contains(out, "chat-api: sse end") {
		t.Fatalf("a discarded terminal must not be logged as a delivery: %s", out)
	}
}

// A run with a live sink still logs a delivery.
func TestComplete_LogsEndWhenAttached(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	rec := newSafeResponseRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := &runState{
		id:             "run_attached",
		user:           "u1",
		conversationID: "conv_1",
		channelKey:     "ch1",
		created:        time.Now(),
		done:           make(chan pendingResult, 1),
		sink:           &sseEventSink{w: sse},
		platform:       &Platform{},
	}
	if !run.complete(pendingResult{answer: "delivered ok"}) {
		t.Fatal("complete should succeed once")
	}
	out := buf.String()
	if !strings.Contains(out, "chat-api: sse end") || !strings.Contains(out, "terminal=message_end") {
		t.Fatalf("expected end log for attached complete: %s", out)
	}
	if strings.Contains(out, "chat-api: sse discarded") {
		t.Fatalf("attached delivery must not be logged as discarded: %s", out)
	}
}

func TestComplete_LogsLatencyStages(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	created := time.Now().Add(-2 * time.Second)
	rec := newSafeResponseRecorder()
	sse, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	run := &runState{
		id:             "run_stages",
		user:           "u1",
		conversationID: "conv_1",
		channelKey:     "ch1",
		created:        created,
		firstFlushedAt: created.Add(300 * time.Millisecond),
		done:           make(chan pendingResult, 1),
		sink:           &sseEventSink{w: sse},
		platform:       &Platform{},
	}
	if !run.complete(pendingResult{answer: "ok"}) {
		t.Fatal("complete should succeed once")
	}
	out := buf.String()
	if !strings.Contains(out, "chat-api: sse end") {
		t.Fatalf("missing end log: %s", out)
	}
	if !strings.Contains(out, "first_delta_ms=300") {
		t.Fatalf("missing first_delta_ms stage: %s", out)
	}
	if !strings.Contains(out, "stream_ms=") {
		t.Fatalf("missing stream_ms stage: %s", out)
	}
	if !strings.Contains(out, "e2e_ms=2") {
		t.Fatalf("missing e2e_ms stage: %s", out)
	}
}

func TestComplete_DiscardedWithoutFlushOmitsStreamStages(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	run := &runState{
		id:             "run_no_flush",
		user:           "u1",
		conversationID: "conv_1",
		channelKey:     "ch1",
		created:        time.Now().Add(-500 * time.Millisecond),
		done:           make(chan pendingResult, 1),
		platform:       &Platform{},
	}
	run.detach()
	if !run.complete(pendingResult{answer: "never seen"}) {
		t.Fatal("complete should succeed once")
	}
	out := buf.String()
	if !strings.Contains(out, "chat-api: sse discarded") {
		t.Fatalf("missing discarded log: %s", out)
	}
	if !strings.Contains(out, "e2e_ms=") {
		t.Fatalf("discarded log should still carry e2e_ms: %s", out)
	}
	if strings.Contains(out, "first_delta_ms") || strings.Contains(out, "stream_ms") {
		t.Fatalf("stream stages must be absent when nothing was flushed: %s", out)
	}
}

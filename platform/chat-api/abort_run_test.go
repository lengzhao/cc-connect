package chatapi

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// newTestRun registers a pending run with a live (non-detached) sink stub.
func newTestRun(t *testing.T, p *Platform, id string) *runState {
	t.Helper()
	run := &runState{
		id:             id,
		user:           "u1",
		conversationID: "conv_1",
		channelKey:     "ch1",
		sessionKey:     "chat-api:ch1:conv_1",
		created:        time.Now(),
		notify:         make(chan struct{}, 1),
		done:           make(chan pendingResult, 1),
		platform:       p,
	}
	if !p.pending.create(run) {
		t.Fatalf("could not register run %s", id)
	}
	return run
}

// A turn torn down mid-flight must terminate its run with an error so the SSE
// stream closes. Before this fix the run stayed pending and the caller hung.
func TestOnProcessingEnd_AbortedFailsPendingRun(t *testing.T) {
	p := &Platform{pending: newPendingStore(8)}
	run := newTestRun(t, p, "run_aborted")

	err := p.OnProcessingEnd(context.Background(), run.replyContext(), core.ProcessingEndEvent{
		Kind:   core.ProcessingEndAborted,
		Reason: "agent session closed while idle-reaping",
	})
	if err != nil {
		t.Fatalf("OnProcessingEnd: %v", err)
	}

	select {
	case result := <-run.done:
		if result.err == nil {
			t.Fatal("aborted turn must yield an error result, got success")
		}
		if result.err.Error() != "agent session closed while idle-reaping" {
			t.Fatalf("unexpected error text: %v", result.err)
		}
		if terminalName(result) != "error" {
			t.Fatalf("terminal should be error, got %q", terminalName(result))
		}
	case <-time.After(time.Second):
		t.Fatal("aborted turn produced no terminal result — the caller would hang")
	}
}

// An abort with no reason still terminates the run.
func TestOnProcessingEnd_AbortedWithoutReason(t *testing.T) {
	p := &Platform{pending: newPendingStore(8)}
	run := newTestRun(t, p, "run_aborted_noreason")

	if err := p.OnProcessingEnd(context.Background(), run.replyContext(),
		core.ProcessingEndEvent{Kind: core.ProcessingEndAborted}); err != nil {
		t.Fatalf("OnProcessingEnd: %v", err)
	}
	select {
	case result := <-run.done:
		if result.err == nil || result.err.Error() != "agent session terminated" {
			t.Fatalf("expected default reason, got %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("no terminal result")
	}
}

// A normal (command) completion must keep producing a success terminal.
func TestOnProcessingEnd_CommandStillSucceeds(t *testing.T) {
	p := &Platform{pending: newPendingStore(8)}
	run := newTestRun(t, p, "run_cmd")
	run.setStreamContent("", "the answer")

	if err := p.OnProcessingEnd(context.Background(), run.replyContext(),
		core.ProcessingEndEvent{Kind: core.ProcessingEndCommand}); err != nil {
		t.Fatalf("OnProcessingEnd: %v", err)
	}
	select {
	case result := <-run.done:
		if result.err != nil {
			t.Fatalf("command completion must not error: %v", result.err)
		}
		if terminalName(result) != "message_end" {
			t.Fatalf("terminal should be message_end, got %q", terminalName(result))
		}
	case <-time.After(time.Second):
		t.Fatal("no terminal result")
	}
}

// Aborting a run that already finished is a no-op, not a double-terminal.
func TestOnProcessingEnd_AbortAfterCompletionIsNoop(t *testing.T) {
	p := &Platform{pending: newPendingStore(8)}
	run := newTestRun(t, p, "run_done")
	p.pending.finish(run.id, pendingResult{answer: "already answered"})

	if err := p.OnProcessingEnd(context.Background(), run.replyContext(),
		core.ProcessingEndEvent{Kind: core.ProcessingEndAborted, Reason: "too late"}); err != nil {
		t.Fatalf("OnProcessingEnd: %v", err)
	}
	select {
	case result := <-run.done:
		if result.err != nil {
			t.Fatalf("original success result must survive, got error %v", result.err)
		}
		if result.answer != "already answered" {
			t.Fatalf("unexpected answer %q", result.answer)
		}
	default:
		t.Fatal("expected the original result to still be buffered")
	}
	select {
	case extra := <-run.done:
		t.Fatalf("a second terminal result was emitted: %+v", extra)
	default:
	}
}

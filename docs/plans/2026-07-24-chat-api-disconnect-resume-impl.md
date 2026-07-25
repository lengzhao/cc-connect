# chat-api 断链重连 Implementation Plan

> **For Claude:** Use TDD; keep disconnect ≠ cancel.

**Goal:** Implement SSE resume via `POST /chat-messages` with `run_id`, detached sink caching, and question webhook notify.

**Architecture:** `runEventSink` (live SSE vs detached virtual); `lastRecoverableEvent`; finish always `complete`+`delete`.

**Tech Stack:** Go, platform/chat-api, httptest tests

---

### Task 1: Design docs

Done: `docs/plans/2026-07-24-chat-api-disconnect-resume-design.md`

### Task 2: Failing tests then implement

Files:
- Create: `platform/chat-api/resume_test.go`
- Create: `platform/chat-api/sink.go`
- Modify: `platform/chat-api/run.go`, `chat.go`, `interaction.go`, `chatapi.go`, `options.go`
- Docs: `docs/chat-api.zh-CN.md`, `docs/chat-api.md`, `config.example.toml`

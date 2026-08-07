# chat-api SSE Lifecycle Logging Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Info slog for SSE start, disconnect, resume, and end (single line per event).

**Architecture:** Central `logSSELifecycle` helper; call from chat.go lifecycle points. Docs note under disconnect resume.

**Tech Stack:** Go `log/slog`, existing chat-api platform package

---

### Task 1: Helper + unit test

**Files:**
- Create: `platform/chat-api/lifecycle_log.go`
- Create: `platform/chat-api/lifecycle_log_test.go`

**Steps:** helper formats `chat-api: sse <event>` with common attrs; test with capture handler.

### Task 2: Wire call sites

**Files:**
- Modify: `platform/chat-api/chat.go`

**Steps:** start / disconnect / resume / end as in design doc.

### Task 3: Docs

**Files:**
- Modify: `docs/chat-api.zh-CN.md`, `docs/chat-api.md`
- Link design from zh-CN related plans line; short ops note under disconnect resume.

# chat-api AskUserQuestion History Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Persist structured ask-user Q&A as normal session history turns for chat-api.

**Architecture:** Capability interface `AskUserQuestionHistoryRecorder`; chat-api always returns true; Engine writes synthetic assistant/user pairs in `finalizeAskQuestion` after successful completion.

**Tech Stack:** Go, existing Session/HistoryEntry, pairHistory

---

### Task 1: Interface + chat-api

**Files:**
- Modify: `core/interfaces.go`
- Modify: `platform/chat-api/interaction.go`
- Test: `platform/chat-api/interaction_test.go`

**Steps:** Add interface; chat-api `RecordAskUserQuestionHistory() bool { return true }`; assert type implements it.

### Task 2: Format + record helper + finalizeAskQuestion

**Files:**
- Modify: `core/engine.go` (`finalizeAskQuestion`, helpers)
- Test: `core/engine_test.go`

**Steps:** TDD — failing tests first for recorder on/off, label vs value, RespondPermission failure; then implement `formatAskQuestionHistoryText` + `maybeRecordAskUserQuestionHistory`.

### Task 3: Docs

**Files:**
- Modify: `docs/chat-api.md`, `docs/chat-api.zh-CN.md`
- Verify design doc matches code

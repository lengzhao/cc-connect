# Minimal Agent System Prompt Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将 cc-connect 内置 Agent system prompt 精简为仅包含两个 chat-api MCP 工具的使用说明。

**Architecture:** 保持现有 prompt 注入、文件缓存和自定义 prompt 拼接机制不变，只缩减
`core.AgentSystemPrompt()` 返回内容。通过正向和负向断言锁定最小 prompt 边界。

**Tech Stack:** Go、标准库 `testing`

---

### Task 1: 锁定最小 prompt 行为

**Files:**
- Modify: `core/engine_test.go`

**Step 1: Write the failing test**

新增测试，断言 prompt 包含 `cc_connect_ask_user`、`cc_connect_client_flow` 和必要字段，
同时不包含 `cc-connect send`、`cc-connect cron`、`cc-connect timer`、
`cc-connect relay`、`NO_REPLY`、`You are running inside cc-connect`。

**Step 2: Run test to verify it fails**

Run:

```bash
go test ./core/ -run TestAgentSystemPrompt_ContainsOnlyStructuredInteractionGuidance -v
```

Expected: FAIL，指出当前 prompt 仍包含已禁止的命令指引。

### Task 2: 精简 prompt

**Files:**
- Modify: `core/interfaces.go`
- Modify: `core/engine_test.go`

**Step 1: Write minimal implementation**

将 `AgentSystemPrompt()` 返回值缩减为 `cc_connect_ask_user` 与
`cc_connect_client_flow` 两节，并更新函数注释。删除或合并仅验证附件、音视频等已移除
内容的旧测试。

**Step 2: Run focused tests**

Run:

```bash
go test ./core/ -run 'TestAgentSystemPrompt' -v
```

Expected: PASS。

### Task 3: Verify integration

**Files:**
- Verify: `core/interfaces.go`
- Verify: `core/engine_test.go`
- Verify: `agent/claudecode/session_test.go`

**Step 1: Run affected package tests**

```bash
go test ./core/ ./agent/claudecode/
```

Expected: PASS。

**Step 2: Run full tests**

```bash
go test ./...
```

Expected: PASS。

**Step 3: Check lints**

检查已修改 Go 文件没有新增诊断。

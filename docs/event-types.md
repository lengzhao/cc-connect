# EventType — Agent 输出事件类型

> 源码位置：`core/message.go:261`

## 概述

`EventType` 是 agent（如 Claude Code）向 engine 发送输出时使用的**消息类型标签**。
Agent 把它的输出流切成一个个 `Event` 结构体，通过 channel 发给 engine，engine 根据类型做不同处理。

## 各类型说明

### `EventText` — 中间/最终文本片段

- **来源**：agent 解析 Claude Code stdout（JSON stream），遇到 `text` 类型 content block 时 emit；也在捕获到 `session_id` 时 emit 一个空内容的该类型（只为传递 SessionID）
- **engine 处理**：把每个 chunk append 到 `textParts[]` 用于流式预览（打字机效果）；`EventResult` 到来时如果 Content 为空就 fallback 到 `textParts` 合并值
- **存在原因**：Claude Code 是流式输出的，文本一段一段来，需要实时渲染进度

### `EventToolUse` — 工具调用信息

- **来源**：agent 解析到 `tool_use` content block 时产生，包含 ToolName 和可读的 ToolInput 摘要
- **engine 处理**：记录工具名到 `toolsUsed[]` 供日志使用；向用户显示"正在用 X 工具"的进度提示
- **存在原因**：用户需要知道 Claude 在做什么（比如"正在写文件"），而不是干等

### `EventToolResult` — 工具执行结果

- **来源**：agent 解析到 `tool_result` content block 后 emit，包含 ToolStatus、ToolExitCode、ToolSuccess
- **engine 处理**：记录工具执行状态，用于最终 reply footer 里显示（几个工具跑成功/失败）
- **存在原因**：展示工具链的执行摘要，帮用户理解这一轮 agent 做了哪些事情

### `EventResult` — 最终聚合结果

- **来源**：agent 在 Claude Code 的 `result` 类型 JSON 行到达时 emit，携带完整的 Content（最终回复文本）和 token 用量
- **engine 处理**：这是一轮对话的终点信号。收到后：把 Content 发给用户、写入会话历史、重置状态、关闭 workspace turn
- **存在原因**：标志"这一轮 agent 交互已完成"，engine 需要明确的 done 信号来做收尾工作

### `EventError` — 发生错误

- **来源**：stderr 输出非空、stdout 读取失败，或 agent 进程异常退出时 emit
- **engine 处理**：把错误文本发送给用户，标记 `eventsNeedResync = true`，然后退出本次事件循环
- **存在原因**：错误需要单独类型，避免和正常文本混淆，engine 可以针对性地做错误处理和状态回滚

### `EventPermissionRequest` — agent 请求用户权限

- **来源**：Claude Code 通过 stdio 协议要求用户确认某个敏感操作（比如执行 shell 命令），agent 解析到对应 JSON 后 emit，包含 RequestID、ToolName 和原始 ToolInputRaw
- **engine 处理**：
  - 有活跃用户轮：向用户发送权限确认消息，等待用户回复 yes/no，再调 `agentSession.RespondPermission()`
  - 无活跃用户轮（后台）：如果开了 `/yolo` 就自动 allow，否则自动 deny 并通知用户
- **存在原因**：Claude Code 的权限确认是异步的——agent 进程阻塞等待结果，engine 需要把这个"暂停点"转发给消息平台，让用户在 Feishu/Telegram 上点按钮来解锁

### `EventThinking` — 思考/推理状态

- **来源**：Claude Code 输出含 `thinking` block 时 emit（扩展思考功能）
- **engine 处理**：仅用于 UI 展示（"Claude 正在思考..."），不影响最终文本或历史记录
- **存在原因**：Claude 3.7+ 支持 extended thinking，思考内容不应混入最终回复，需要单独类型以便 engine 选择性展示或抑制

## 整体流动图

```
Claude Code stdout
  │
  ▼ agent/claudecode/session.go 解析 JSON lines
EventThinking        → engine 显示"思考中"
EventText            → engine 流式预览
EventToolUse         → engine 显示"使用工具X"
EventToolResult      → engine 记录工具状态
EventPermissionRequest → engine 转发给用户确认
EventResult          → engine 发送最终回复 + 写历史   ← 一轮结束
EventError           → engine 报错 + 退出
```

## 设计动机

Agent 是异步流式进程，engine 是同步的消息路由器。需要一套结构化的事件协议，把非结构化的进程输出翻译成消息平台（Feishu/Telegram 等）可以响应的动作。

## 相关代码

| 文件 | 说明 |
|------|------|
| `core/message.go:261` | EventType 常量定义及 Event 结构体 |
| `agent/claudecode/session.go` | 产生各类 Event 的解析逻辑 |
| `core/engine.go:4792` | engine 主事件循环对各类型的处理 |

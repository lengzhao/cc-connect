# chat-api Platform — API v1

> 版本：**v1.0.0-draft**（2026-06-29）  
> 状态：规范草案 — `platform/chat-api` 已实现 v1  
> 平台类型：`chat-api`（`[[projects.platforms]] type = "chat-api"`）  
> 实现说明见 [chat-api 平台设计](./plans/2026-06-29-chat-api-platform-design.md)

## 1. 概述

`chat-api` 是 cc-connect 的一种 **Platform**，对外提供 HTTP API，供自定义 App / BFF 直连，无需开放 Management API。

**v1 能力**

- 会话列表、重命名、删除
- 会话历史（游标分页）
- 发送消息：**SSE 流式**（`message` → `text_delta` → `message_end`）
- 隐式创建会话（首条 `chat-messages` 不带 `conversation_id`）
- 多客户端并发；会话忙时 **排队**（复用 Engine，默认 `busy_policy=queue`）

```mermaid
flowchart LR
  App[App / BFF]
  API["chat-api\n/v1/*"]
  Eng[cc-connect Engine]
  Agent[Coding Agent]

  App --> API
  API --> Eng
  Eng --> Agent
  Agent --> Eng
  Eng --> API
  API -->|SSE| App
```

---

## 2. 基础约定

### 2.1 Base URL

默认 `listen_addr`（如 `:8030`），API 前缀 `path`（默认 `/v1/`）。

```text
https://api.example.com/v1/conversations
```

下文路径均相对于 API 前缀。

### 2.2 协议与编码

| 项 | 约定 |
|----|------|
| 请求/响应体 | `application/json; charset=utf-8` |
| 流式响应 | `text/event-stream; charset=utf-8` |
| 时间戳 | Unix **秒**（整数） |
| 字符集 | UTF-8 |

### 2.3 响应信封

与 Bridge 一致：

```json
{"ok": true, "data": { ... }}
{"ok": false, "error": "conversation not found"}
```

HTTP 状态码表示成功/失败类别；`error` 为可读说明。常见错误码见 §2.7。

### 2.4 用户标识 `user`

终端用户 ID（字符串，1–128 字符，建议 `[a-zA-Z0-9_\-:.]+`）。

| 场景 | 传递方式 |
|------|----------|
| 读（GET/DELETE） | Query `user=` **或** 配置的头 `user_header`（默认 `X-Chat-API-User`）二选一 |
| 写（POST/PATCH） | **仅** `user_header`（由 BFF 注入） |

无法解析 `user` → `400`，`error` 含 `user required`。

平台不校验调用方是否「拥有」该 `user`；对外暴露时 BFF 须先鉴权再注入头，勿把 `api_token` 交给终端。

### 2.5 认证

配置 `api_token`（别名 `token`）后，所有请求须：

```http
Authorization: Bearer <api_token>
```

无效或缺失 → `401`。

### 2.6 分页

统一 **游标分页**：

| 参数 | 默认 | 说明 |
|------|------|------|
| `limit` | 20 | 1–100 |
| `cursor` | — | 上一页返回的 `next_cursor`；首页省略 |
| `has_more` | — | 响应字段 |
| `next_cursor` | — | 有下一页时返回；否则省略 |

会话列表默认按 `updated_at` 降序。消息历史首页为**最新** N 条，`cursor` 向更早翻页。

### 2.7 常见错误

| HTTP | 典型 `error` 片段 | 说明 |
|------|-------------------|------|
| 401 | `unauthorized` | Token 无效 |
| 400 | `user required` | 缺少 user |
| 400 | `invalid request` | JSON / 参数错误 |
| 404 | `not found` | 会话或游标不存在 |
| 409 | `conversation busy` | 仅 `busy_policy=reject` |
| 413 | `payload too large` | 请求体过大 |
| 500 | `internal error` | 服务器错误 |

### 2.8 message_id（v1）

`message_id` 由 API 层确定性派生，历史与 SSE 一致：

```text
message_id = "{conversation_id}:{turn_index}"
```

`turn_index` 从 `0` 起，按会话内完整 user→assistant 对递增。未完成轮次不出现在历史 API 中。

---

## 3. 数据模型（v1）

仅包含**有实际值**的字段；未列出的字段 v1 不返回。

### 3.1 Conversation

```json
{
  "id": "s1a2b3c",
  "name": "代码解释",
  "last_message_preview": "这段代码实现了……",
  "created_at": 1780000000,
  "updated_at": 1780003600
}
```

| 字段 | 说明 |
|------|------|
| `id` | 会话 ID（`conversation_id`） |
| `name` | 展示名称 |
| `last_message_preview` | 最后一条内容摘要（≤200 字符）；无历史时省略 |
| `created_at` / `updated_at` | Unix 秒 |

### 3.2 Message

一轮用户提问 + 助手回复。

```json
{
  "id": "s1a2b3c:0",
  "query": "帮我解释这段代码",
  "answer": "这段代码实现了……",
  "created_at": 1780003500
}
```

| 字段 | 说明 |
|------|------|
| `id` | 见 §2.8 |
| `query` / `answer` | 用户输入与完整助手回复 |
| `created_at` | 用户消息时间（Unix 秒） |

### 3.3 发送消息请求体

```json
{
  "conversation_id": "s1a2b3c",
  "query": "帮我解释这段代码",
  "inputs": [],
  "auto_generate_name": true,
  "metadata": {}
}
```

| 字段 | 必填 | 说明 |
|------|------|------|
| `conversation_id` | 否 | 省略 → **隐式创建**新会话 |
| `query` | 是 | 用户文本 |
| `inputs` | 否 | 多模态附件（见 §3.4） |
| `auto_generate_name` | 否 | 新会话首条消息后：取 `query` 截断 32 字符为标题 |
| `metadata` | 否 | 传入 hooks（`HookContext`），不进 agent prompt |

`user` 由 `user_header` 提供，不在 body 中重复。

### 3.4 Input（多模态）

```json
{
  "type": "image",
  "transfer_method": "base64",
  "data": "<base64>",
  "mime_type": "image/png",
  "filename": "screenshot.png"
}
```

v1 至少支持 `base64`。`type`：`image` | `file` | `audio`。

---

## 4. API 端点

### 4.1 会话列表

```http
GET /conversations?limit=20
GET /conversations?cursor=s0old&limit=20
Authorization: Bearer <api_token>
X-Chat-API-User: user_001
```

响应：

```json
{
  "ok": true,
  "data": {
    "limit": 20,
    "has_more": true,
    "next_cursor": "s0older",
    "conversations": [
      {
        "id": "s1a2b3c",
        "name": "代码解释",
        "last_message_preview": "这段代码实现了……",
        "created_at": 1780000000,
        "updated_at": 1780003600
      }
    ]
  }
}
```

**切换会话**（无专用 API）：选中 `conversation_id` → `GET …/messages` → 后续 `chat-messages` 携带该 id。

---

### 4.2 更新会话（重命名）

```http
PATCH /conversations/{conversation_id}
Authorization: Bearer <api_token>
X-Chat-API-User: user_001
Content-Type: application/json
```

```json
{
  "name": "代码解释"
}
```

仅支持改 `name`。响应 `data` 含 `id`、`name`、`updated_at`。

---

### 4.3 删除会话

```http
DELETE /conversations/{conversation_id}
Authorization: Bearer <api_token>
X-Chat-API-User: user_001
```

```json
{"ok": true, "data": {"result": "success"}}
```

---

### 4.4 历史消息

```http
GET /conversations/{conversation_id}/messages?limit=20
GET /conversations/{conversation_id}/messages?cursor=s1a2b3c:5&limit=20
Authorization: Bearer <api_token>
X-Chat-API-User: user_001
```

响应：

```json
{
  "ok": true,
  "data": {
    "limit": 20,
    "has_more": false,
    "messages": [
      {
        "id": "s1a2b3c:1",
        "query": "帮我解释这段代码",
        "answer": "这段代码实现了……",
        "created_at": 1780003500
      }
    ]
  }
}
```

---

### 4.5 发送消息（SSE）

```http
POST /chat-messages
Authorization: Bearer <api_token>
X-Chat-API-User: user_001
Content-Type: application/json
Accept: text/event-stream
```

请求体见 §3.3。带 `conversation_id` 时，平台先 `SwitchSession` 再派发。

#### 会话忙（`busy_policy`，默认 `queue`）

| 策略 | 行为 |
|------|------|
| `queue`（默认） | 消息进入 Engine 队列，同一 Claude Code session 在上轮结束后继续处理；SSE 立即返回 `message_queued` 后结束连接 |
| `reject` | 返回 `409`，`error`: `conversation busy` |

`message_queued` 示例：

```text
event: message_queued
data: {"message_id":"s1a2b3c:2","queue_depth":1}
```

客户端可在上轮 `message_end` 后重试，或轮询 `GET …/messages` 等待新轮次出现。

队列满时 SSE 返回 `event: error`，`data.error` 说明队列已满（对齐 Engine `MsgQueueFull`）。

#### SSE 规范事件（v1）

| event | 说明 |
|-------|------|
| `message` | 轮次开始：`conversation_id`、`message_id`、`run_id` |
| `text_delta` | 正文增量：`text` |
| `message_end` | 结束：完整 `answer` |
| `error` | 不可恢复错误 |
| `message_queued` | 仅 `busy_policy=queue` 且会话忙时 |

**示例（正常流式）**

```text
event: message
data: {"conversation_id":"s1a2b3c","message_id":"s1a2b3c:1","run_id":"run_abc"}

event: text_delta
data: {"message_id":"s1a2b3c:1","text":"这段代码"}

event: message_end
data: {"message_id":"s1a2b3c:1","conversation_id":"s1a2b3c","answer":"这段代码实现了……"}
```

隐式创建会话时，`message` / `message_end` 携带新 `conversation_id`。

**客户端断开**：HTTP/SSE 连接关闭后，**后台 agent 轮次继续执行**；已生成内容仍会写入历史。如需中止，调用 `POST /runs/{run_id}/cancel`（见 §4.6）。

> **扩展事件**（`tool_call_*`、`agent_thought`、业务 `metadata`）不在 v1 规范内；若实现可作为非保证附加事件，见设计文档。

---

### 4.6 取消进行中的轮次

`run_id` 由 `POST /chat-messages` 的 SSE `message` 事件返回。

```http
POST /runs/{run_id}/cancel
Authorization: Bearer <api_token>
X-Chat-API-User: user_001
```

响应：

```json
{"ok": true, "data": {"result": "success"}}
```

- 校验 `run_id` 属于该 `user`；否则 `404`
- 停止 Engine 当前交互轮次（等同内部 `/stop` 语义，保留会话可继续发消息）
- 若 SSE 仍连接，发送 `event: error`，`data.error` 为 `canceled by user`

---

## 5. 接口清单（v1）

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/conversations` | 列表 |
| `PATCH` | `/conversations/{id}` | 重命名 |
| `DELETE` | `/conversations/{id}` | 删除 |
| `GET` | `/conversations/{id}/messages` | 历史 |
| `POST` | `/chat-messages` | 发消息（SSE） |
| `POST` | `/runs/{run_id}/cancel` | 取消进行中的轮次 |

**v1 不提供**

- `POST /conversations`（用 `chat-messages` 隐式创建）
- `response_mode=blocking`（由 BFF 消费 SSE 聚合，或 v1.1 再加）
- 独立 `POST …/name`（合并为 `PATCH`）
- 顶层 `GET /messages`（嵌套在会话下）

---

## 6. 配置示例

```toml
[[projects.platforms]]
type = "chat-api"

[projects.platforms.options]
listen_addr = ":8030"
path = "/v1/"
public_url = "https://api.example.com"
api_token = "your-service-token"
user_header = "X-Chat-API-User"
cors_origins = ["https://app.example.com"]
request_timeout = "30m"
busy_policy = "queue"    # queue | reject
```

---

## 7. 版本历史

| 版本 | 日期 | 说明 |
|------|------|------|
| v1.0.0-draft | 2026-06-29 | 瘦身后 v1：5 端点、SSE-only、queue 默认、嵌套 messages |

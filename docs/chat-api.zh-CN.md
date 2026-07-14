# chat-api Platform — API v1

> 版本：**v1.1.1**（2026-07-14）  
> 状态：已实现 — 与 `platform/chat-api` 对齐  
> 平台类型：`chat-api`（`[[projects.platforms]] type = "chat-api"`）  
> 设计说明：[chat-api 平台设计](./plans/2026-06-29-chat-api-platform-design.md)

## 1. 概述

`chat-api` 是 cc-connect 的一种 **Platform**，对外提供 HTTP + SSE API，供自定义 App / BFF 直连，无需开放 Management API。

**v1 能力**

- 会话列表、重命名、删除
- 会话历史（游标分页）
- 发送消息：**SSE 流式**（`message` → `thinking_delta?` → `text_delta` → `message_end`）
- 用户确认窗口：权限 / AskUserQuestion；公共 respond 字段、`ping` 保活、单槽 supersede
- 隐式创建会话（首条 `chat-messages` 不带 `conversation_id`）
- 会话忙时排队（默认 `busy_policy=queue`，复用 Engine 队列）

```mermaid
flowchart LR
  Client[App / BFF]
  API["chat-api /v1/*"]
  Eng[Engine]
  Agent[Agent]

  Client --> API
  API --> Eng
  Eng --> Agent
  Agent --> Eng
  Eng --> API
  API -->|SSE| Client
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
| 非流式 REST | `application/json; charset=utf-8` |
| 流式聊天 | HTTP `200` + `text/event-stream`（**不是** JSON 信封） |
| 时间戳 | Unix **秒**（整数） |

REST 成功/失败使用 JSON 信封；`POST /chat-messages` 成功时 body 为 SSE。流内错误为 `event: error`（HTTP 仍为 200）。

### 2.3 响应信封

```json
{"ok": true, "data": { ... }}
{"ok": false, "error": "not found"}
```

### 2.4 认证与用户

两层身份：

| 层 | 方式 | 作用 |
|----|------|------|
| 服务 | `Authorization: Bearer <api_token>` | 调用方鉴权（BFF / 后端） |
| 终端用户 | `user` | 创建者 / 发送者 |
| 工作区频道 | `channel`（可选） | multi-workspace 绑定键；共享/隔离 `work_dir` |

> **生产环境必须配置 `api_token`（别名 `token`）。** 未配置时跳过 Bearer 校验。

平台不校验 `user` 归属；`api_token` 勿下发终端，由 BFF 鉴权后注入 `user`。

**会话模型**

| 概念 | 行为 |
|------|------|
| 创建 | 首条 `POST /chat-messages` 不带 `conversation_id` |
| 列表 | `GET /conversations` 仅返回该 user **创建**的会话 |
| 参与 | 持有 `conversation_id` 即可发消息、读历史（不必在列表中） |
| Engine | `session_key = chat-api:{channel}:{conversation_id}`（`channel` 省略时为内部 `__default__`；`conversation_id` 仍为 API 侧的 `conv_*`） |
| 工作区 | 可选 `channel` → `Message.ChannelKey`，供 Engine multi-workspace 解析 `work_dir`；未传则使用项目默认 `work_dir` |
| 管理 | 重命名 / 删除仅**创建者**（owner）可操作 |

`conversation_id` 与 `channel` **正交**：前者决定 agent 对话上下文，后者决定工作目录绑定（同 channel 下多个 conversation 可共享目录）。未传 `X-Chat-API-Channel` 时，chat-api 自动将项目默认 `work_dir` 绑定到内部默认 channel，无需额外配置。

在 `mode = "multi-workspace"` 下，显式 `X-Chat-API-Channel` 会作为 channel 名称参与 Engine 约定匹配。chat-api 在消息进入 Engine 前会尝试自动初始化：若 platform options 配置了 `base_dir`（或与项目一致的 `cc_base_dir` / 环境变量 `AGENT_WORK_DIR`），且 `cc_data_dir`、`cc_project` 已设置，则会创建 `<base_dir>/<channel>` 并写入 `workspace_bindings.json`，普通消息（如 `hi`）可直接进入 agent，而不会被误判为本地目录路径。未配置 platform `base_dir` 时，行为与 IM 平台一致：目录不存在则进入 workspace 初始化/绑定引导，SSE 会在提示结束后正常返回 `message_end`。

**`user` 传递**

| 场景 | 需要 `user`？ | 方式 |
|------|---------------|------|
| `GET /conversations` | 是 | `user_header` 或 `?user=` |
| `GET …/messages` | 否 | `api_token` + `conversation_id` |
| `POST /chat-messages` | 是 | `user_header` |
| `PATCH` / `DELETE` | 是（须 owner） | `user_header` |
| `POST …/cancel` | 是（须发起者） | `user_header` |
| `POST …/interactions/…/respond` | 是（须发起者） | `user_header` |

默认 `user_header = X-Chat-API-User`，`user_name_header = X-Chat-API-User-Name`（可选，仅发消息），`channel_header = X-Chat-API-Channel`（可选，仅 `POST /chat-messages`、`POST …/cancel` 与交互响应透传）。`user` 为 1–128 字符，`[a-zA-Z0-9_\-:.]+`；`channel` 为 1–256 字符，`[a-zA-Z0-9_\-:./]+`，且不得包含 `.`、`..`、空路径段或以 `/` 开头/结尾。

历史按轮次返回 `user_id` / `user_name`（有记录时）。v1 不返回 `owner_id`。

### 2.5 分页

| 参数 | 默认 | 说明 |
|------|------|------|
| `limit` | 20 | 1–100 |
| `cursor` | — | 上一页 `next_cursor` |
| `has_more` | — | 是否有下一页 |

会话列表按 `updated_at` 降序。消息历史首页为最新 N 条，向更早翻页。

### 2.6 常见错误

**REST**

| HTTP | `error` | 说明 |
|------|---------|------|
| 401 | `unauthorized` | Token 无效 |
| 400 | `user required` | 缺少 user |
| 400 | `invalid request` | 参数错误 |
| 400 | `exactly one of decision, option_id, option_ids, answer required` | 确认回传字段互斥/缺省 |
| 400 | `invalid decision` / `unknown option` | 确认回传内容无效 |
| 404 | `not found` | 资源不存在、无权，或 interaction 已被 supersede |
| 409 | `conversation busy` | `busy_policy=reject` 时会话忙 |
| 409 | `interaction already responded` | 交互已响应 |
| 409 | `interaction expired` | 交互已过期 |
| 413 | `payload too large` | 请求体 > 10 MiB |
| 500 | `internal error` | 服务器错误 |

**SSE**（`event: error`）

| `data` | 说明 |
|--------|------|
| `{"error":"canceled by user"}` | 用户取消 |
| `{"error":"request timed out"}` | 超过 `request_timeout` |
| `{"error":"interaction timed out","kind":"question"}` | AskUserQuestion 超过 `interaction_timeout`（当前 turn 已取消） |
| `{"error":"too many concurrent requests"}` | 超过 `max_runs` |
| （队列满文案） | Engine 队列已满 |

### 2.7 message_id

```text
message_id = "{conversation_id}:{turn_index}"
```

`turn_index` 从 0 起，按完整 user→assistant 对递增。`event: message` 时即分配。未完成轮次不出现在历史中。

---

## 3. 数据模型

仅返回有值的字段。

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
| `id` | `conversation_id` |
| `name` | 展示名称 |
| `last_message_preview` | 最后一条 history 摘要（≤200 rune）；无历史时省略 |
| `created_at` / `updated_at` | Unix 秒 |

### 3.2 Message

```json
{
  "id": "s1a2b3c:0",
  "query": "帮我解释这段代码",
  "answer": "这段代码实现了……",
  "created_at": 1780003500,
  "user_id": "user_001",
  "user_name": "Alice"
}
```

| 字段 | 说明 |
|------|------|
| `id` | 见 §2.7 |
| `query` / `answer` | 用户输入与助手完整回复 |
| `created_at` | 用户消息时间 |
| `user_id` / `user_name` | 该轮发送者（有记录时返回） |

`thinking_delta` 不写入历史。

### 3.3 发送消息

**请求体**

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
| `conversation_id` | 否 | 省略则隐式创建 |
| `query` | 是 | 用户文本 |
| `inputs` | 否 | 多模态附件（§3.4）；历史不 replay |
| `auto_generate_name` | 否 | 默认 `true`；新会话首条后取 query 前 32 rune 为标题 |
| `metadata` | 否 | 传入 hooks，不进 prompt，不在响应中返回 |

`user` 由 header 提供，不在 body 中。

**SSE 响应**（`Accept: text/event-stream`、`*/*` 或省略）

```text
event: message
data: {"conversation_id":"s1a2b3c","message_id":"s1a2b3c:1","run_id":"run_abc"}

event: thinking_delta
data: {"message_id":"s1a2b3c:1","text":"分析代码结构…"}

event: text_delta
data: {"message_id":"s1a2b3c:1","text":"这段代码实现了……"}

event: message_end
data: {"message_id":"s1a2b3c:1","conversation_id":"s1a2b3c"}
```

| event | `data` | 说明 |
|-------|--------|------|
| `message` | `conversation_id`, `message_id`, `run_id` | 轮次开始 |
| `thinking_delta` | `message_id`, `text` | 推理增量（可选） |
| `text_delta` | `message_id`, `text` | 正文增量 |
| `permission_request` | 见 §3.5 | 工具权限确认窗口 |
| `question_request` | 见 §3.5 | AskUserQuestion 确认窗口 |
| `interaction_superseded` | `interaction_id`, `replacement_id`, `run_id`, `message_id` | 同一 run 上新确认替换旧确认 |
| `interaction_ack` | `interaction_id`, `message_id`, `text?` | 用户已响应确认的回执（可选） |
| `ping` | `run_id`, `ts` | SSE 保活；可忽略 |
| `message_end` | `message_id`, `conversation_id`, `answer?` | 轮次结束 |
| `message_queued` | `message_id`, `queue_depth` | 会话忙且 `busy_policy=queue` |
| `error` | `error`, `kind?` | 错误（§2.6） |

`message_end.answer` 默认省略（`include_answer_in_message_end = true` 时附带）。

隐式创建时 `conversation_id` 在 `message` 事件中返回。`busy_policy=reject` 且会话忙时返回 `409` JSON。

**客户端注意**

- 拼接 `text_delta` 得完整回复；断开 SSE **不**停止 agent，内容仍写入 history
- `message_queued` 后勿立即重开 SSE；等上轮结束或轮询 history
- 收到 `permission_request` / `question_request` 时弹出确认窗口；用 `expires_at` 倒计时；通过 §4.7 回传结果，**不要**把确认结果当普通 `chat-messages`
- `ping` 为保活事件，客户端可忽略
- 同一 run 若出现新的确认，会先发 `interaction_superseded`；旧 `interaction_id` 不可再 respond
- AskUserQuestion 超时后当前阻塞 turn 会取消；后续用户输入应重新 `POST /chat-messages`，作为普通对话
- 取消：`POST /runs/{run_id}/cancel`（`run_id` 来自 `message` 事件）

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

`transfer_method` 仅支持 `base64`。`type`：`image` | `file` | `audio`（audio 每请求最多一条）。

### 3.5 用户确认窗口

Agent 在工具权限或 AskUserQuestion 时，SSE 中插入结构化交互事件。两类确认都保持原 SSE **打开**，App 弹窗后调用 §4.7 回传；确认后同一 SSE 继续 `text_delta` → `message_end`。

同一时刻每个 run **最多一个**未决 interaction。若同 turn 再次弹出确认，会先发 `interaction_superseded`，再发新的 `permission_request` / `question_request`。

无 `perm:` / `askq:` 结构化按钮的普通卡片按纯文本 `Reply`，**不会**当作确认窗口。

**`permission_request`**

```text
event: permission_request
data: {
  "interaction_id":"ix_abc",
  "run_id":"run_abc",
  "message_id":"s1a2b3c:1",
  "prompt":"Allow tool Bash?",
  "expires_at":1780004100,
  "actions":[
    {"id":"allow","label":"Allow"},
    {"id":"deny","label":"Deny"},
    {"id":"allow_all","label":"Allow All"}
  ]
}
```

**`question_request`**

```text
event: question_request
data: {
  "interaction_id":"ix_abc",
  "run_id":"run_abc",
  "message_id":"s1a2b3c:1",
  "prompt":"选择部署环境",
  "expires_at":1780004100,
  "actions":[
    {"id":"0:1","label":"Staging"},
    {"id":"0:2","label":"Production"}
  ]
}
```

| 字段 | 说明 |
|------|------|
| `interaction_id` | 本次交互 ID（回传时必填） |
| `expires_at` | Unix 秒；过期后前端应禁用按钮（服务端以 interaction 状态为准） |
| `actions` | 可选按钮；`id` 为公共 ID（权限：`allow`/`deny`/`allow_all`；问答：`0:1`），与 §4.7 的 `decision` / `option_id` 对齐 |

**超时**（`interaction_timeout`，默认 `10m`，且不超过当前 run 剩余 `request_timeout`）

| 类型 | 行为 |
|------|------|
| `permission_request` | 超时自动 `deny`；agent 收到拒绝结果后可自行收束；SSE 可继续至 `message_end` |
| `question_request` | 超时取消当前阻塞 turn（`/stop`）；SSE `error` 含 `kind=question`；后续输入为普通新对话 |

`expires_at` 仅为客户端提示；安全边界是 run / interaction 是否仍有效、user 是否匹配。

---

## 4. API 端点

示例省略 `Authorization: Bearer <api_token>`。

### 4.1 会话列表

```http
GET /conversations?limit=20
X-Chat-API-User: user_001
```

也支持 `?user=user_001`。

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

### 4.2 重命名

```http
PATCH /conversations/{conversation_id}
X-Chat-API-User: user_001
Content-Type: application/json

{"name": "代码解释"}
```

须 owner。响应 `data`：`id`、`name`、`updated_at`。

### 4.3 删除

```http
DELETE /conversations/{conversation_id}
X-Chat-API-User: user_001
```

须 owner。`{"ok": true, "data": {"result": "success"}}`

### 4.4 历史消息

```http
GET /conversations/{conversation_id}/messages?limit=20
GET /conversations/{conversation_id}/messages?cursor=s1a2b3c:5&limit=20
```

持有 `conversation_id` 即可，不需要 `user`。

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
        "created_at": 1780003500,
        "user_id": "user_001",
        "user_name": "Alice"
      }
    ]
  }
}
```

### 4.5 发送消息

```http
POST /chat-messages
X-Chat-API-User: user_001
X-Chat-API-User-Name: Alice
X-Chat-API-Channel: team-alpha/backend
Content-Type: application/json
Accept: text/event-stream
```

`X-Chat-API-Channel` 可选。省略时使用项目默认 `work_dir`；填写时写入 Engine `ChannelKey`。在 `mode = "multi-workspace"` 下，若 platform 配置了 `base_dir`（及 `cc_data_dir`、`cc_project`），chat-api 会自动创建 `<base_dir>/<channel>` 并持久化绑定；否则按约定匹配已有目录，未匹配则进入 workspace 初始化/绑定流程。取消进行中的轮次时，若当时请求携带了 channel，cancel 会一并透传。

请求体与 SSE 见 §3.3。

| `busy_policy` | 行为 |
|---------------|------|
| `queue`（默认） | 入队；SSE 返回 `message_queued` 后关闭 |
| `reject` | `409`，`error`: `conversation busy` |

### 4.6 取消轮次

```http
POST /runs/{run_id}/cancel
X-Chat-API-User: user_001
```

`run_id` 来自 SSE `message` 事件。校验归属 user，否则 `404`。等同 Engine `/stop`，会话可继续发消息。

```json
{"ok": true, "data": {"result": "success"}}
```

SSE 仍连接时发送 `event: error`，`data.error` 为 `canceled by user`。

### 4.7 响应确认窗口

```http
POST /runs/{run_id}/interactions/{interaction_id}/respond
X-Chat-API-User: user_001
Content-Type: application/json

{"decision":"allow"}
```

用于响应 `permission_request` 和 `question_request`。响应后原 SSE 继续等待 agent 输出。

推荐公共字段（互斥，恰好一个）：

```json
{"decision":"allow"}
{"decision":"deny"}
{"decision":"allow_all"}
{"option_id":"0:2"}
{"option_ids":["0:1","0:3"]}
{"answer":"自由文本"}
```

| 字段 | 说明 |
|------|------|
| `decision` | 权限结果：`allow` / `deny` / `allow_all` |
| `option_id` | 单选问答；`"0:2"` 映射为内部 `askq:0:2` |
| `option_ids` | 多选问答；映射为 Engine 编号列表（如 `1,3`） |
| `answer` | 自由文本答案 |

归属 user 须与发起 `chat-messages` 的 user 一致，否则 `404`。已响应 → `409 interaction already responded`；已过期 → `409 interaction expired`；已被 supersede → `404`。

```json
{"ok": true, "data": {"result": "success"}}
```

响应成功后，原 SSE 可发送 `interaction_ack`，随后 agent 继续输出 `thinking_delta` / `text_delta`，最终 `message_end`。确认回执**不会**提前结束轮次。

---

## 5. 接口清单

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/conversations` | 列表 |
| `PATCH` | `/conversations/{id}` | 重命名 |
| `DELETE` | `/conversations/{id}` | 删除 |
| `GET` | `/conversations/{id}/messages` | 历史 |
| `POST` | `/chat-messages` | 发消息（SSE） |
| `POST` | `/runs/{run_id}/cancel` | 取消轮次 |
| `POST` | `/runs/{run_id}/interactions/{interaction_id}/respond` | 响应确认窗口 |

**v1 不提供**：`POST /conversations`、`response_mode=blocking`、历史附件 replay、`tool_call_*` SSE、`/health`。

---

## 6. 配置

```toml
[[projects.platforms]]
type = "chat-api"

[projects.platforms.options]
listen_addr = ":8030"
path = "/v1/"
api_token = "your-service-token"
user_header = "X-Chat-API-User"
user_name_header = "X-Chat-API-User-Name"
channel_header = "X-Chat-API-Channel"
cors_origins = ["https://app.example.com"]
request_timeout = "30m"
interaction_timeout = "10m"
sse_ping_interval = "15s"
busy_policy = "queue"
include_answer_in_message_end = false
max_runs = 1000
run_ttl = "2h"
```

| 选项 | 默认 | 说明 |
|------|------|------|
| `listen_addr` / `listen` | `:8030` | 监听地址 |
| `path` | `/v1/` | API 前缀 |
| `api_token` / `token` | 空 | Bearer token；空则跳过认证 |
| `user_header` | `X-Chat-API-User` | 终端 user header |
| `user_name_header` | `X-Chat-API-User-Name` | 可选显示名 header |
| `channel_header` | `X-Chat-API-Channel` | 可选工作区 channel header |
| `cors_origins` | 空 | CORS 允许来源 |
| `request_timeout` / `timeout` | `30m` | SSE 等待上限 |
| `interaction_timeout` | `10m` | 确认窗口超时；不超过当前 run 剩余 `request_timeout` |
| `sse_ping_interval` | `15s` | SSE 保活间隔；`0` / `0s` 关闭 |
| `busy_policy` | `queue` | `queue` 或 `reject` |
| `include_answer_in_message_end` | `false` | `message_end` 是否附带 answer |
| `max_runs` | `1000` | 内存 pending run 上限 |
| `run_ttl` | `2h` | run 记录 TTL |

会话持久化由 Engine `sessions.json` 承担；`pendingStore` 为进程内内存态（确认窗口不支持多副本共享）。

---

## 7. 版本历史

| 版本 | 日期 | 说明 |
|------|------|------|
| v1.1.1 | 2026-07-14 | 确认窗口硬化：公共 `decision`/`option_id(s)`、SSE actions 公共 id、`ping`、`interaction_superseded`、结构化超时错误 |
| v1.1.0 | 2026-07-14 | 用户确认窗口：`permission_request` / `question_request`、交互响应端点、`interaction_timeout` |
| v1.0.0 | 2026-07-09 | 精简规范；新增可选 `X-Chat-API-Channel` |
| v1.0.0-draft | 2026-06-29 | 初版：6 端点、SSE-only、queue 默认 |

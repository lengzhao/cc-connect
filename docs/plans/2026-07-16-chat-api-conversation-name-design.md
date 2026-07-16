# chat-api 会话 Name 接口设计

> Date: 2026-07-16  
> Status: implemented

## Goal

为 chat-api 提供单会话详情查询，并增强现有 `name` / `auto_generate_name` 能力。对外继续复用旧字段名，不引入 `title` 或 `auto_generate_title`。

## API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/conversations/{id}` | 查询会话详情（须 owner） |
| `POST` | `/conversations/{id}/name/generate` | 异步 AI 生成 name（须 owner） |

详情响应字段与列表项一致：`id`、`name`、`last_message_preview`、`created_at`、`updated_at`。

`POST .../name/generate` 请求体：

```json
{"force": false}
```

响应 `202 Accepted`：

```json
{"name_run_id": "name_run_abc", "status": "running"}
```

前端轮询 `GET /conversations/{id}` 读取更新后的 `name`。

## auto_generate_name

`POST /chat-messages` 继续复用 `auto_generate_name`，仅对省略 `conversation_id` 的新会话首轮生效。

| `auto_generate_name_mode` | `auto_generate_name=true` 行为 |
|---------------------------|--------------------------------|
| `heuristic`（默认） | 首条 query 截断 32 rune（现有行为） |
| `ai` | 首轮对话结束后后台 AI 生成 name |

手动 `PATCH /conversations/{id}` 优先级最高。`force=false` 的生成不会覆盖已有非 `default` name。

## 实现要点

- name 持久化在 `core.Session.Name`。
- AI 生成通过 Engine handler 发起内部 turn，使用 `Message.SkipHistory` 避免内部 prompt 与回复写入用户可见 history。
- name 生成使用独立 `nameReplyContext`，不占用聊天 SSE run。
- 默认 `auto_generate_name_mode = "heuristic"`，不改变现有部署体验。

## Docs / migration

- 更新 `docs/chat-api.zh-CN.md`、`docs/chat-api.md`
- 更新 `config.example.toml` 注释
- 更新 `scripts/local-chat-api-verify.sh`

## Tests

- `GET /conversations/{id}` owner 校验与字段
- `POST .../name/generate` 异步写回 name
- 默认 heuristic 行为不变
- `ai` 模式下新会话 `auto_generate_name=true` 触发后台生成

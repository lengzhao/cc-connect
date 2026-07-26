# chat-api 创建空会话接口设计

> Date: 2026-07-26  
> Status: implemented

## Goal

允许客户端在发送首条消息前显式创建空会话，并指定会话 `name`。

## API

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/conversations` | 创建空会话（须 `user`） |

请求体：

```json
{"name": "我的新会话"}
```

- `name` 可选；省略或空白时使用 `"default"`（与隐式创建首条消息前的初始名一致）
- 非空白 `name` 直接写入 `core.Session.Name`，不触发 `auto_generate_name`

响应 `201 Created`，字段与 `GET /conversations/{id}` 一致：

```json
{
  "ok": true,
  "data": {
    "id": "conv_abc123",
    "name": "我的新会话",
    "created_at": 1780000000,
    "updated_at": 1780000000
  }
}
```

空会话无历史，因此不含 `last_message_preview`。

## 行为

- owner：`sessionKey = chat-api:{user}`
- 生成 opaque `conv_*` id，与隐式创建相同
- 不启动 Agent、不占用 run 槽
- 后续 `POST /chat-messages` 携带返回的 `conversation_id` 即可在该空会话中发首条消息
- 隐式创建（省略 `conversation_id`）行为不变

## Tests

- 带 `name` 创建成功并出现在列表
- 省略 `name` 默认为 `default`
- 缺少 `user` 返回 400
- 创建后可 `GET` 详情、`POST /chat-messages` 发首条消息

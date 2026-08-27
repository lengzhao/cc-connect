# Engine：注入 message_id 到 Agent 提示头

## 背景

cc-connect 在将用户消息转发给 Agent 时，会在内容前拼接一段元数据头（`[cc-connect ...]`），包含发送者、平台、chat_id 等信息（由 `injectSender` 配置控制）。

部分 Agent 工具（如 Feishu 的 `lark_send`）支持 `reply_to_message_id` 参数，用于将回复消息关联到特定的原始消息，形成 thread 结构。过去 Agent 需要额外调用接口才能获取当前消息的 ID，增加了一次不必要的查询。

## 改动

在 `buildAgentPrompt` 中新增 `messageID` 参数。当 `injectSender` 启用且 `messageID` 非空时，在提示头中追加：

```
message_id=om_xxxxxxxxxxxx
```

完整示例：

```
[cc-connect sender="Alice" platform=feishu chat_id=oc_xxx message_id=om_yyy]
用户消息内容
```

所有三处调用点（`processInteractiveMessageWith`、`processInteractiveEvents` 队列处理、`drainPendingMessages`）均已更新，分别透传 `msg.MessageID` / `queued.messageID`。

相关提交：`d8f17750`

## 使用方式

Agent 从提示头解析出 `message_id` 后，可直接传给支持 thread 回复的工具：

```python
# 伪代码
message_id = parse_header("message_id")  # om_xxxx
lark_send(chat_id=..., content=..., reply_to_message_id=message_id)
```

这样 Agent 无需额外查询消息历史，即可准确回复到对应的消息 thread。

## 注意事项

- 仅在 `injectSender = true` 时注入，默认配置不受影响
- `messageID` 为空时不追加该字段，不影响不支持 thread 回复的平台

# Feishu WebSocket 消息可靠性改进

## 背景

Feishu 平台通过 WebSocket 长连接向 Bot 推送事件消息。在实际运行中发现两类问题：

1. **WS 假死**：连接表面存活，但消息推送已停止（无 ping/pong 心跳异常，但 read 永远阻塞）。服务端不会主动断开，导致 Bot 长时间收不到消息。
2. **重启/断连期间消息丢失**：WS 重连或服务重启时，这段时间内发送的 @ 消息不会被补发，永久丢失。

## 方案

### 1. WS 假死检测（SetReadDeadline）

在 lark SDK 的 WebSocket 客户端中，每次收到消息后重置读超时（`SetReadDeadline`）。若超时时间内没有收到任何数据（包括 ping frame），则主动断开并触发重连。

配合 SDK 新增的 `Close()` 方法，`Platform.Stop()` 可以优雅关闭 WS 连接，而不是依赖 context 取消。

相关提交：`1cd7b0e` `e2a3c700`

### 2. catch-up 补偿轮询

新增 `platform/feishu/catchup.go`，实现基于 REST API 的消息补偿机制：

- 每 **3 分钟**轮询一次指定的 chat 列表（`catchup_chats` 配置项）
- 拉取最近 **5 分钟**（去掉最后 30 秒缓冲）内的消息
- 过滤出含有 Bot @ 的消息，跳过已在 dedup 缓存中的消息
- 将漏掉的消息重新注入正常的消息处理流程

dedup TTL 同步从默认值延长至 **10 分钟**，确保 WS 推送与 catch-up 拉取之间不会重复处理同一条消息。

相关提交：`a276c32a` `9284e9d1` `80cdcd4f`

### 3. 未知事件类型不再返回 500

lark SDK 的事件分发器在收到未注册的事件类型时会返回 HTTP 500，导致 Feishu 服务端误判连接异常并重试。修复为直接返回 200。

相关提交：`e5e86750`

## 配置

在 `config.toml` 的 `[platform.feishu]` 段中添加（仅 WS 主节点需要）：

```toml
# 需要补偿轮询的 chat_id 列表，逗号分隔
catchup_chats = "oc_xxxx,oc_yyyy"
```

catch-up poller 只在 `is_ws_primary = true` 时启动，HTTP 模式或 WS 副节点不需要配置。

## 时序示意

```
WS 推送正常时:
  Feishu → WS → dedup(TTL=10min) → Engine

WS 假死/断连后:
  Feishu → REST API ← catch-up poller(每3分钟)
                            ↓
                       dedup 过滤（已处理的跳过）
                            ↓
                          Engine
```

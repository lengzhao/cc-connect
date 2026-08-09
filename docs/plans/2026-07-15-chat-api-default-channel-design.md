# chat-api 默认 channel 设计

> Date: 2026-07-15  
> Status: superseded (2026-08-09)

## Current behavior (2026-08-09)

未传 / 空的 `X-Chat-API-Channel` 时，**不再**分配共享的 `default_channel`，而是以 **`user` 作为 channel**：

| 请求 channel | 结果 |
|---|---|
| 省略 / 空 | `ChannelKey = user`，session key = `chat-api:{user}:{conv_id}`，工作目录 `<base_dir>/<user>` |
| 显式合法名（含 `default_channel`） | 原样使用 → `<base_dir>/<channel>` |
| 非法字符 / 路径逃逸 | `400 invalid request` |

访客与创建者共享同一 agent 上下文时，需显式传入相同的 `X-Chat-API-Channel`；仅省略 header 时各自使用自己的 `user` 作为 channel。

## Superseded: shared default_channel (2026-07-15 – 2026-08-09)

曾短暂改为省略时不填充任何 channel；此前更早版本会自动分配 `default_channel`，导致多用户共用工作目录。

## Original design (2026-07-15)

取消内部哨兵 `__default__`，省略 header 时分配 `default_channel`。此行为已于 2026-08-09 撤销。

# chat-api 默认 channel 设计

> Date: 2026-07-15  
> Status: implemented

## Goal

取消内部哨兵 `__default__`。未传 / 空的 `X-Chat-API-Channel` 时，API 入口直接分配显式默认频道 `default_channel`，并走与普通 channel 相同的 workspace 解析与绑定。不对方 `__default__` 做保留名特判；仅保留通用 channel 名有效性校验。

## Behavior

| 请求 channel | 结果 |
|---|---|
| 省略 / 空 | `ChannelKey = "default_channel"`，session key = `chat-api:default_channel:{conv_id}` |
| `default_channel`（显式） | 同省略，合法 |
| 非法字符 / 路径逃逸（`..`、绝对路径等） | `400 invalid request` |
| 其他合法名 | 原样使用 → `<base_dir>/<channel>` |

默认工作目录：`<base_dir>/default_channel`（不再映射到 `base_dir` 根）。

## Base dir injection

`mode = "multi-workspace"` 时，`cmd/cc-connect` 通过 `buildPlatformOptions` 注入 `cc_base_dir = proj.BaseDir`。chat-api `ensureChannelWorkspace` 据此创建并绑定 channel 目录。Platform options 显式 `base_dir` 优先于 `cc_base_dir`。

## Validation

入口 `resolveChannel`：

1. 空 → 返回 `""`（由 `channelKeyForMessage` 填 `default_channel`）
2. `validChannel`（字符集 + 长度）
3. `isSafeWorkspaceChannelPath`（路径段不得为 `.` / `..` / 空；非绝对路径）

`ResolveChannelName` 对 `default_channel` 返回自身（非 `"."`）。

## Docs / migration

- 更新 `docs/chat-api.zh-CN.md`、`docs/plans/2026-06-29-chat-api-platform-design.md`
- 已绑定 `chat-api:__default__` 的旧会话不会自动迁移

## Tests

- 空 header → `ChannelKey == "default_channel"`
- `buildPlatformOptions` 在 multi-workspace 注入 `cc_base_dir`
- e2e：仅有 `cc_base_dir`（无 platform `base_dir`）时自动创建 `<base_dir>/default_channel`

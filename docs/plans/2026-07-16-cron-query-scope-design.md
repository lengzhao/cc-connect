# Cron 查询隔离设计

> Date: 2026-07-16  
> Status: implemented

## Goal

多工作目录 / 多 channel 场景下，cron 查询默认按当前会话上下文隔离，避免 `chat-api:default_channel:conv_xxx` 与 `chat-api:team/backend:conv_yyy` 互相看到对方的定时任务。需要全局视图时显式传 `all=true`。

## Scope model

| 查询参数 | 行为 |
|---|---|
| `session_key` | 仅返回该会话下的任务（最高优先级） |
| `all=true` | 返回全部任务 |
| `project` | 返回该项目下全部任务（跨 channel/会话） |
| 无参数 | 返回全部任务（兼容旧行为） |

优先级：`session_key` > `all=true` > `project` > 全量。

## Affected surfaces

- 聊天 `/cron`：已按 `Message.SessionKey` 隔离，不变。
- 本地 socket API `GET /cron/list`
- 管理 API `GET /api/v1/cron`
- CLI `cc-connect cron list --session-key ...` / `CC_SESSION_KEY` / `--all`

## Multi-workspace examples

| 上下文 | session_key 示例 |
|---|---|
| chat-api 默认 channel | `chat-api:default_channel:conv_abc123` |
| chat-api 命名 channel | `chat-api:team/backend:conv_xyz789` |
| Feishu 频道 | `feishu:oc_xxx:ou_yyy` |

创建任务时 `session_key` 已写入 `CronJob`；查询侧只需按同一 key 过滤。

## Compatibility

- 不传 `session_key` 的管理后台 / 脚本仍可 `?all=true` 或 `?project=` 获取更广范围。
- 不修改 `jobs.json` 存储格式，无需迁移。

## Tests

- `TestQueryCronJobs_*` in `core/cron_test.go`
- `TestHandleCronList_SessionKeyScope` in `core/api_test.go`
- `TestMgmt_CronList_SessionKeyScope` in `core/management_test.go`
- `TestBuildCronListURL_*` in `cmd/cc-connect/cron_list_test.go`

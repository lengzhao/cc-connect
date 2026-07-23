# chat-api AskUserQuestion 写入历史对话

> Date: 2026-07-23  
> Status: approved

## Goal

在 **chat-api** 场景下提供可配置开关：开启后，将 `AskUserQuestion` 的问题与用户确认结果写入 Session 历史，使 `GET /conversations/{id}/messages` 能以普通对话轮次回放该交互。

## Non-goals

- 不改变默认行为（开关默认关闭）
- 不扩展历史 API schema（不新增 `ask_user_questions` 字段）
- 不把 permission 确认窗口写入历史
- 不在 core 硬编码 `"chat-api"` 平台名
- 不改变提交给 Agent 的 `answers`（仍优先使用 option `value`）

## Config

`[[projects.platforms]] type = "chat-api"` 的 options：

```toml
[projects.platforms.options]
ask_user_question_history = true   # default false
```

仅影响该 chat-api 实例；其他平台不受影响。

## Architecture

```mermaid
flowchart TD
  agent[Agent AskUserQuestion] --> eng[Engine]
  eng --> sse[SSE question_request]
  user[App respond] --> eng
  eng --> dual[Parse agent_value + display_value]
  dual --> agentResp[RespondPermission with agent_value]
  agentResp -->|success and switch on| hist[Append history pairs]
  hist --> save[SessionManager.Save]
  hist --> pair[pairHistory query/answer]
```

### Capability interface（core）

```go
// AskUserQuestionHistoryRecorder is implemented by platforms that want
// AskUserQuestion Q&A persisted as normal session history turns.
type AskUserQuestionHistoryRecorder interface {
    RecordAskUserQuestionHistory() bool
}
```

chat-api 根据 `ask_user_question_history` option 返回 `true/false`。Engine 通过类型断言启用，不判断平台名称。

### History shape（synthetic turns）

开启后，一次用户请求若触发 AskUserQuestion，历史顺序为：

1. `user` = 原始用户 query（已有）
2. 对每个问题（多题按回答顺序）：
   - `assistant` = 可读问题文本（标题 / 问题 / 描述 / 选项 label）
   - `user` = 用户看到的 label，或自定义输入原文
3. `assistant` = Agent 最终回复（已有）

`pairHistory` 无需改 schema，自然配对为：

| query | answer |
|-------|--------|
| 原始请求 | AskUserQuestion 问题1 |
| 用户选择1 | AskUserQuestion 问题2（或多题时下一问） |
| … | … |
| 最后一题选择 | Agent 最终回复 |

单题时即为：`原始请求 → 问题`、`用户选择 → 最终回复`。

### Dual-value answer parsing

| 用途 | 来源 |
|------|------|
| 提交 Agent（`agent_value`） | option `value`，空则回退 `label`；自定义输入用原文 |
| 写入历史（`display_value`） | option `label`；自定义输入用原文 |

现有 `resolveAskQuestionAnswer` 继续产出 `agent_value`；新增并行解析 `display_value`，仅在落历史时使用。

### Write timing

1. SSE 发出问题时 **不**写历史。
2. 多题期间暂存 `(question, display_value)`。
3. **全部题目答完且 `RespondPermission` 成功** 后，按顺序一次性 `AddHistory`：
   - assistant(question text) → user(display_value) × N
4. 然后解除 pending，让 Agent 继续产出最终回复（最终 assistant 自然排在后面）。
5. 每次写入后 `sessions.Save()`。

### Failure / skip rules

整组 AskUserQuestion **不写历史** 当：

- 开关关闭
- 任一题未完成（超时 / 取消 / 用户放弃）
- `RespondPermission` 失败

禁止写入半组残缺配对。

### Question text format（assistant history）

建议可读纯文本，例如：

```text
选择账户

请选择要操作的账户

已开户账户列表

选项：
1. ★ 推荐 · 工资卡 (尾号 1234)
2. 理财卡 (尾号 5678)
```

字段优先级：`Title`/`Header` → `Question` → `Description` → 编号选项（用 label，推荐项可带推荐标记）。不写入 option `value`。

## Compatibility

- 默认 `false`：现有行为与测试不变。
- 历史 API 仍返回 `query`/`answer`/`user_id`/`user_name`/`created_at`。
- IM 平台不实现该接口 → 永不落 AskUserQuestion 历史。

## Docs

- `docs/chat-api.md` / `docs/chat-api.zh-CN.md`：说明开关与历史回放形态
- `config.example.toml`：注释示例

## Testing

- 默认关闭：AskUserQuestion 不增加历史条目
- 显式开启：单题写入 2 条（assistant 问题 + user label）再接最终回复
- 多题：按序写入多组配对
- label/value 分离：历史用 label，Agent 收到 value
- 自定义输入：历史与 Agent 均为原文
- `RespondPermission` 失败 / 超时：不落库
- 重启后 `GET .../messages` 仍可读

    # Amber Fork 改造清单

本文记录 Amber 内部维护的 cc-connect 分支，相对上游（GitHub `lengzhao/cc-connect`）做了哪些改造。

- **对比基线**：上游 `custom-features` 分支 HEAD `907ecdc1`（`fix(feishu): respond to nexCallback within Feishu 3s budget`）
- **对比对象**：Amber `merge/github-into-amber` 分支 HEAD
- **共同祖先**：`907ecdc1` —— 即上游的 GitHub 改动已全部合入 Amber，本文列出的均为 **Amber 独有**改动
- **规模**：24 个提交；代码 19 个文件 +684 / −89，含文档共 26 个文件 +1469 / −97

> 复现方式：`git diff 907ecdc1..HEAD`（在 Amber 仓库内）。
> GitHub 侧的 13 个提交（nexCallback/LTS、`/send`、skill-guide 路由、timer 开关等）已合入，不属本文范围。

---

## 一、改动总览

| 分类 | 提交 | 目的 |
|---|---|---|
| Agent 元数据注入 | `d8f17750` `ab9b510c` `0392b03c` | 让 Agent 看到 `message_id` / `bot_mentioned` / `root_id` |
| 提示注入防护 | `ab9b510c` | 转义用户内容中的 `[cc-connect`，防止伪造元数据头 |
| 飞书 WS 可靠性 | `1cd7b0e4` `e2a3c700` `e5e86750` `79836c5b` | 假死检测、优雅关闭、未知事件不再 500 |
| 飞书消息补偿 | `a276c32a` `9284e9d1` `80cdcd4f` | catch-up 轮询找回断连期间丢失的 @ 消息 |
| Claude Code 图片处理 | `87114baf` `884ec83a` | 超 2000px 图片自动降采样 |
| 展示控制 | `58e22fb4` | 新增 `hide_intermediate_text`，只投递最终结果 |
| 依赖与品牌 | `d6b6391a` `fcf85275` `2749cb20` `73f09f4e` | oapi-sdk-go 私有 fork、`[amber-fork]` 日志标识 |

---

## 二、Agent 元数据注入（核心改造）

### 背景

cc-connect 会把用户消息包上一层元数据头再发给 Agent：

```
[cc-connect sender_id=ou_xxx sender_name="Alice" platform=feishu chat_id=oc_yyy]
用户消息内容
```

上游已有的字段（`sender_id` / `sender_name` / `sender_email` / `platform` / `chat_id`，以及可选的 `inject_timestamp`、`agent_context`）由 `inject_sender` 配置控制。

问题在于：Agent 虽然知道「谁在哪个群说话」，却**不知道当前这条消息本身是谁**。具体表现为：

1. **无法精确回复到某条消息** —— Feishu 的 `lark_send` 支持 `reply_to_message_id`，Agent 只能先额外查一次消息历史。
2. **无法区分「明确 @bot」和「群里的环境闲聊」** —— 当 `group_reply_all = true`（或 `require_mention = false`）时，所有群消息都会进入 Agent，Agent 没有任何信号判断哪条是真正在问它。
3. **无法定位线程根** —— 线程隔离模式（`thread_isolation`）下，Agent 不知道当前会话对应的 thread root。

### 改动内容

在 `buildAgentPrompt` 中新增 3 个字段。三处调用点均已透传：

| 字段 | 来源 | 注入条件 |
|---|---|---|
| `message_id` | `Message.MessageID` | 非空即注入 |
| `root_id` | 从 `sessionKey` 解析 | sessionKey 为线程格式时注入 |
| `bot_mentioned=true` | `Message.BotMentioned` | 值为 true 时注入 |

示例输出：

```
[cc-connect sender_id=ou_xxx platform=feishu chat_id=oc_yyy message_id=om_zzz root_id=om_zzz bot_mentioned=true]
用户消息内容
```

### 三个字段的实现细节

#### 1. `message_id`

上游 `Message.MessageID` **字段早已存在**（`core/message.go:214`），但只用于内部追踪——去重重投递判定、消息撤回探测、卡片更新、以及发给 hook 端点的 `HookEvent`。**从未进入 Agent prompt**。

改动：在 `queuedMessage` 与 `state` 上新增 `messageID` 字段（engine.go:3186、3204），从 `msg.MessageID` 透传至 `buildAgentPrompt`。

详见 [engine-message-id-injection.md](engine-message-id-injection.md)。

#### 2. `root_id`

上游**已经算出过线程根 ID**，但完全封装在 feishu 平台包内：

- `makeSessionKey` 生成 `feishu:<chatID>:root:<rootID>`（feishu.go:3434-3439）
- `parseThreadRootID` / `isThreadSessionKey` 反向解析（feishu.go:3722、3739）
- 用于线程隔离会话、`ReconstructReplyCtx` 回填、`RelayGroupVisibilityKey` 路由

core 包只知道 `sessionKey` 是个不透明字符串，第 3 段始终当作黑盒。

改动：在 core 新增 `extractThreadRootID`（engine.go:16624），识别 `root:` / `thread:` 两种前缀，语义与 feishu 的 `parseThreadRootID` 等价。

采用 `SplitN(key, ":", 4)` 按整数下标解析，而非 `strings.HasPrefix`，目的是不让 core 依赖平台实现（符合项目「core 不认平台名」的约束）。**代价是同一套前缀约定在 core 与 feishu 各维护一份，后续改动需同步。**

#### 3. `bot_mentioned`

上游 `isBotMentioned` 只作为**准入过滤器**：群聊消息没 @bot 就直接丢弃（feishu.go:1362）。`Message` 结构体上**没有对应字段**，布尔值用完即弃。而 `group_reply_all = true` 时过滤器被完全绕过，Agent 无从分辨。

改动链路：

1. `core/message.go` 新增 `Message.BotMentioned` 字段
2. feishu `onMessage` 计算一次布尔值（feishu.go:1439）
3. 作为新参数穿过 `dispatchMessage` 签名，在 **9 个构造 `Message` 的分支**逐个赋值（回复卡、富文本、图片、文件、音频、@all 等）
4. engine 经 `queuedMessage` 带到 `buildAgentPrompt`，为 true 时追加 `bot_mentioned=true`

### 元数据传递通道

核查了全部 6 条可能路径，确认**只有 prompt 文本头这一条通道**：

| 通道 | 机制 | 是否携带这 3 个字段 |
|---|---|---|
| **prompt 文本头** | `buildAgentPrompt` 拼接前缀，随 `Send(prompt,…)` 作为用户消息内容 | ✅ **唯一通道** |
| system prompt | `AgentSystemPrompt()` 经 `setupMemoryFile` 写入 Agent 记忆文件 | ❌ 仅讲 CLI 用法 |
| 环境变量 | `core.MergeEnv` | ❌ 仅有 `CC_CONNECT_PERMISSION_HOOK_SKIP=1` |
| AgentContext | `Language` / `TaskID` / `TraceID` / `Custom` | ❌ 调用方（chat-api）传入的业务字段 |
| hooks | `HookEvent` JSON POST 到 HTTP handler | ⚠️ 含 `message_id`，但发给外部系统，不进 Agent |
| CLI | `cc-connect send/cron/timer` | ❌ 仅有 `--session <key>` |

### 提示注入防护

新增 `bot_mentioned=true` 这类**会被 Agent 信任的字段**带来新风险：用户可以在消息正文里伪造一行假 header 来欺骗 Agent。

因此 `buildAgentPrompt` 在拼接前对用户内容做了转义：

```go
safeContent := strings.ReplaceAll(content, "[cc-connect", `\[cc-connect`)
```

---

## 三、飞书 WebSocket 可靠性

### 1. WS 假死检测

连接表面存活但消息推送已停止（read 永久阻塞，无 ping/pong 异常），服务端不主动断开，Bot 长时间收不到消息。

方案：在 lark SDK 的 WebSocket 客户端中，每次收到消息后重置读超时（`SetReadDeadline`）。超时内无任何数据则主动断开并重连。配合 SDK 新增的 `Close()` 方法，`Platform.Stop()` 可优雅关闭连接。

提交：`1cd7b0e4` `e2a3c700` `79836c5b`（TCP keepalive）
详见 [feishu-ws-reliability.md](feishu-ws-reliability.md)

### 2. catch-up 补偿轮询

WS 重连或服务重启期间发送的 @ 消息不会被补发，永久丢失。

方案：新增 `platform/feishu/catchup.go`，基于 REST API 补偿：

- 每 **3 分钟**轮询 `catchup_chats` 配置的 chat 列表
- 拉取最近 **5 分钟**（去掉最后 30 秒缓冲）内的消息
- 过滤出含 Bot @ 的消息，跳过 dedup 缓存中已处理的
- 重新注入正常消息处理流程

dedup TTL 同步从 60 秒延长至 **10 分钟**（`core/dedup.go`），确保 WS 推送与 catch-up 拉取不会重复处理。

配置（`[platform.feishu]`，仅 WS 主节点需要）：

```toml
catchup_chats = "oc_xxxx,oc_yyyy"
```

只在 `is_ws_primary = true` 时启动。

提交：`a276c32a` `9284e9d1` `80cdcd4f`

### 3. 未知事件类型不再返回 500

lark SDK 事件分发器遇到未注册事件类型时返回 HTTP 500，Feishu 服务端会误判连接异常并重试。修复为返回 200。

提交：`e5e86750`

---

## 四、Claude Code 图片处理

### 问题

Anthropic API 对多图请求有 2000 像素的单边限制。超限时 Claude Code 会注入一段错误上下文而非把图片传给模型，表现为 Agent 拒绝截图。

### 方案

新增 `downscaleImageIfNeeded()`（`agent/claudecode/session.go`）：

- 解码图片，若任一边超过 **1992px**，用 BiLinear 缩放到限制内
- 重新编码为 JPEG（质量 85）
- 支持 PNG / JPEG / WebP（`golang.org/x/image`）
- 解码或编码失败时**回退原图**，不影响发送

同时移除「图片已保存到本地」的文本提示——图片已通过 base64 多模态内容送达，该提示会诱使 Claude Code 用 Read 工具重读文件，而 Read 工具拒绝 >2000×2000px 的图片。

提交：`884ec83a` `87114baf`
详见 [claudecode-image-downscale.md](claudecode-image-downscale.md)

---

## 五、展示控制：`hide_intermediate_text`

新增 `display.hide_intermediate_text` 配置（全局与项目级，项目级优先）。

启用后，Agent 的所有中间叙述文本不再投递，**只有最终 `EventResult` 内容会发给用户**。适用于希望 Agent 只输出结论、不刷屏的场景。

配置：

```toml
[display]
hide_intermediate_text = true
```

对应 `core.DisplayCfg.HideIntermediateText`，`config.EffectiveDisplay` 返回签名同步扩展。

提交：`58e22fb4`

---

## 六、依赖、日志与其他

| 改动 | 说明 | 提交 |
|---|---|---|
| oapi-sdk-go 私有 fork | `replace` 指向 `github.com/decren/oapi-sdk-go/v3`，为 WS 假死检测提供 `Close()` 与 read deadline | `fcf85275` `d6b6391a` |
| `golang.org/x/image` | 图片降采样依赖 | `884ec83a` |
| `[amber-fork]` 日志前缀 | 启动/停止日志加标识，便于区分运行中的二进制是否为 Amber 维护版本 | `87114baf` |
| 移除本地第三方源码 | 清理仓库内的 vendored 代码 | `2749cb20` |
| 忽略本地 Claude 配置 | `.gitignore` 增加 `/.claude/` | `73f09f4e` |

详见 [oapi-sdk-fork.md](oapi-sdk-fork.md)

---

## 七、新增文档

Amber 侧补充的说明文档：

- [claudecode-image-downscale.md](claudecode-image-downscale.md) —— 图片 2000px 限制分析与修复
- [engine-message-id-injection.md](engine-message-id-injection.md) —— `message_id` 注入
- [event-types.md](event-types.md) —— core `EventType` 常量说明
- [feishu-ws-reliability.md](feishu-ws-reliability.md) —— 飞书 WS 可靠性改进
- [oapi-sdk-fork.md](oapi-sdk-fork.md) —— oapi-sdk-go fork 说明
- [superpowers/plans/2026-09-10-merge-github-into-amber.md](superpowers/plans/2026-09-10-merge-github-into-amber.md) —— GitHub 分支合并方案

---

## 附录：Amber 独有提交

```
c46330ee docs: add merge plan for github/custom-features into amber
78faf190 chore: merge github/custom-features into feat/ambre-feedback-aug25
79836c5b feat(feishu): enable TCP keepalive on WebSocket connection
0392b03c feat(engine): inject root_id into agent prompt for thread-isolated sessions
ab9b510c feat(lark): inject bot_mentioned=true into agent prompt header
0dfedb9f docs: add event-types.md explaining core EventType constants
58e22fb4 feat(display): add HideIntermediateText to suppress agent narration
f2ab6178 docs: add change summaries for aug-25 work
a0bfcc1e docs: add claudecode image 2000px limit analysis and fix summary
884ec83a fix(claudecode): downscale images exceeding 2000px before sending to Claude Code
d6b6391a chore(deps): bump oapi-sdk-go to amber-fork 361f56df
73f09f4e ignore local claude settings
87114baf fix(claudecode): don't hint local image path to avoid Read tool size limit
2749cb20 delete local third party source code
fcf85275 chore(deps): upgrade oapi-sdk-go to decren fork v3.10.1-amber.1
e5e86750 fix(deps): silence unregistered WS event types — return 200 instead of 500
e2a3c700 fix(feishu): remove Close() call incompatible with upstream lark SDK
80cdcd4f feat(feishu): wire catch-up poller into platform Start/Stop lifecycle
9284e9d1 feat(feishu): add catch-up polling for missed bot mentions via REST API fallback
a276c32a feat(feishu): extend dedup TTL to 10min; add catch-up config fields to Platform
1cd7b0e4 fix(deps): patch lark SDK — add Close(), read deadline for half-dead WS detection
b2c178f4 Merge remote-tracking branch 'origin/custom-features' into feat/lark-thread-reply
5659a025 feat(context): refresh Automon resources per conversation
d8f17750 feat(engine): inject message_id into cc-connect agent prompt header
```

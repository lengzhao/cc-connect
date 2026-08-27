# oapi-sdk-go Fork 管理

## 背景

cc-connect 依赖 `github.com/larksuite/oapi-sdk-go/v3` 作为 Feishu 的官方 Go SDK。上游版本（v3.5.3）存在三个影响生产稳定性的 bug，且短期内无法等待上游合并修复。

## 修复的 Bug

| Bug | 描述 |
|-----|------|
| A | WebSocket 假死：连接存活但消息推送停止，缺少 read deadline 检测 |
| B | 缺少优雅关闭：无 `Close()` 方法，`Stop()` 无法主动断开 WS 连接 |
| C | 未知事件类型返回 HTTP 500，导致 Feishu 服务端误判并重试 |

## 演进过程

### 阶段一：本地源码 patch（`1cd7b0e`）

将上游 SDK 源码复制到 `third_party/oapi-sdk-go/`，直接修改并通过 `go.mod` 的 `replace` 指令引用：

```
replace github.com/larksuite/oapi-sdk-go/v3 => ./third_party/oapi-sdk-go
```

优点：改动立即生效，无需发布。缺点：源码入库体积大（295 个文件，60 万行），不适合长期维护。

### 阶段二：发布独立 fork（`fcf85275`）

将修改后的代码推送到独立 fork：`github.com/decren/oapi-sdk-go`，以 pseudo-version 发布：

```
github.com/decren/oapi-sdk-go/v3 v3.10.1-amber.1.0.20260825135827-f84ac821c6ab
```

`go.mod` 的 `replace` 指令改为指向该 fork：

```
replace github.com/larksuite/oapi-sdk-go/v3 => github.com/decren/oapi-sdk-go/v3 v3.10.1-amber.1.0...
```

### 阶段三：清理本地源码（`2749cb20`）

删除 `third_party/oapi-sdk-go/` 目录，仓库恢复整洁。依赖完全通过 Go module 管理。

## Breaking Changes（v3.5.3 → v3.10.x）

升级跨越了多个 minor 版本，部分常量被重命名：

| 旧名称 | 新名称 |
|--------|--------|
| `UserIdTypeGetMessageOpenId` | `UserIdTypeOpenId` |
| `FileType{Pdf,Doc,Xls,Ppt,Mp4,Opus,Stream}` | `CreateFileFileType{Pdf,Doc,...}` |
| `ReceiveIdTypeChatId` | `CreateMessageV1ReceiveIDTypeChatId` |

这些改动已在 `platform/feishu/feishu.go` 中全部更新。

## 后续维护

如上游 SDK 合并了相同的修复，可通过以下步骤切回官方版本：

1. 删除 `go.mod` 中的 `replace` 指令
2. `go get github.com/larksuite/oapi-sdk-go/v3@latest`
3. 检查 breaking changes 并更新调用处

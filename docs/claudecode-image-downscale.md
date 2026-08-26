# Claude Code 图片尺寸限制与自动缩图修复

## 问题现象

用户通过 Feishu 发送截图时，ambre-feedback agent 回复：

> 图片分辨率太高了（超过 2000×2000 像素），无法处理。请重新上传一张尺寸较小的截图，我可以帮你看看是什么问题。

## 根本原因

**Anthropic API 的多图片请求限制**：当一次 API 请求包含多张图片时（多轮对话历史中积累了图片），任何单张图片的最长边不得超过 2000px，否则 API 返回：

```
400 {"type":"error","error":{"type":"invalid_request_error",
  "message":"At least one of the image dimensions exceed max allowed size
  for many-image requests: 2000 pixels"}}
```

Claude Code CLI（v2.1.220+）捕获这个 400 错误后，不再将图片传给 LLM，而是向 LLM 注入一段英文上下文：

```
The user sent an image that could not be processed (dimensions exceed 2000x2000px limit).
They're asking about a problem in the image and how to solve it.
```

LLM（claude-haiku）根据这段上下文生成了中文拒绝回复。该限制在 Claude Code 二进制内部硬编码，官方 issue [#1908](https://github.com/anthropics/claude-code/issues/1908) 已被标记为 **not planned**。

## 排查路径

1. LangFuse trace 显示出错 turn 的 `input_tokens: 9`，说明图片未传入 LLM
2. 在 Claude Code 二进制中用 `strings` 找到 `"image dimensions exceed"` 字符串，确认限制内置于 CLI
3. cc-connect 及 agent-runtime 代码中无此限制，系统提示词中也无此指令

## 解决方案

在 `agent/claudecode/session.go` 的 `Send()` 方法中，将图片以 base64 发给 Claude Code **之前**，先检查尺寸并缩放到 ≤ 1992px（安全阈值，留 4px 余量）。

```go
const maxImageDimension = 1992

func downscaleImageIfNeeded(data []byte, mimeType string) ([]byte, string) {
    img, _, err := image.Decode(bytes.NewReader(data))
    if err != nil {
        return data, mimeType // 解码失败原样传递
    }
    w, h := img.Bounds().Dx(), img.Bounds().Dy()
    if w <= maxImageDimension && h <= maxImageDimension {
        return data, mimeType // 无需缩放
    }
    // 等比缩放，长边压到 1992px
    newW, newH := w, h
    if w > h {
        newW = maxImageDimension
        newH = h * maxImageDimension / w
    } else {
        newH = maxImageDimension
        newW = w * maxImageDimension / h
    }
    dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
    draw.BiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
    var buf bytes.Buffer
    jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85})
    return buf.Bytes(), "image/jpeg"
}
```

支持 PNG / JPEG / WebP（需引入 `golang.org/x/image`），解码或编码失败时原样传递（安全降级）。

## 相关 commits

| 仓库 | Commit | 内容 |
|------|--------|------|
| cc-connect | `884ec83a` | 添加 `downscaleImageIfNeeded()`，修复 2000px 限制 |
| agent-runtime | `79016dd` | go.mod 更新 cc-connect 版本至 `884ec83a` |

## 参考

- [Claude Code issue #1908](https://github.com/anthropics/claude-code/issues/1908) — 官方确认但不计划修复
- [Claude Code issue #16173](https://github.com/anthropics/claude-code/issues/16173) — 相关：会话在大图 400 错误后变为不可恢复状态

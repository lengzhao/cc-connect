package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const larkCommentFieldName = "nex_comment"

const nexWorkItemCallbackTimeout = 8 * time.Second

// handleNexWorkItemCardAction records Nex work-item card clicks. Feishu requires
// an HTTP 200 within ~3s, so we return toast + an optimistic result card immediately
// and POST to LTS asynchronously (then PATCH siblings / refresh from cardPatches).
func (p *Platform) handleNexWorkItemCardAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool) {
	if event == nil || event.Event == nil || event.Event.Action == nil || event.Event.Action.Value == nil {
		return nil, false
	}
	value := event.Event.Action.Value
	if !isNexCallbackValue(value) {
		return nil, false
	}
	if strings.TrimSpace(p.ltsWorkItemCallbackURL) == "" {
		slog.Warn(p.tag()+": nexCallback received but lts_work_item_callback_url is not configured")
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: "error", Content: "工单回调未配置，请打开 Nex 收件箱操作。"},
		}, true
	}

	label := mapStringValue(value["label"])
	action := mapStringValue(value["action"])
	if label == "" {
		label = action
	}
	comment := extractNexFormComment(event.Event.Action.FormValue, event.Event.Action.InputValue)
	_, toastContent, banner := resultPresentationForNex("ok", label, comment)

	messageID := ""
	if event.Event.Context != nil {
		messageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}
	workItemID := mapStringValue(value["workItemId"])
	optionID := mapStringValue(value["optionId"])
	idempotencyKey := "lark:" + workItemID + ":" + action + ":" + optionID

	body := map[string]any{
		"tenantId":       mapStringValue(value["tenantId"]),
		"workItemId":     workItemID,
		"action":         action,
		"answer":         value["answer"],
		"comment":        comment,
		"idempotencyKey": idempotencyKey,
		"messageId":      messageID,
		"label":          label,
		"title":          mapStringValue(value["title"]),
		"type":           mapStringValue(value["type"]),
		"details":        mapStringValue(value["details"]),
		"inboxUrl":       mapStringValue(value["inboxUrl"]),
	}

	resp := &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: "success", Content: toastContent},
		Card: &callback.Card{
			Type: "raw",
			Data: buildNexOptimisticResultCard(
				mapStringValue(value["title"]),
				mapStringValue(value["type"]),
				mapStringValue(value["details"]),
				banner,
				mapStringValue(value["inboxUrl"]),
			),
		},
	}

	go p.postNexWorkItemCallbackAsync(body, messageID)

	slog.Warn(p.tag()+": nex work item card action accepted",
		"work_item_id", workItemID,
		"message_id", messageID,
		"action", action,
	)
	return resp, true
}

type nexWorkItemCallbackResponse struct {
	Status      string                 `json:"status"`
	CardPatches []nexWorkItemCardPatch `json:"cardPatches"`
}

type nexWorkItemCardPatch struct {
	MessageID string         `json:"messageId"`
	Card      map[string]any `json:"card"`
}

func (p *Platform) postNexWorkItemCallbackAsync(body map[string]any, clickerMessageID string) {
	parsed, err := p.postNexWorkItemCallbackSync(body)
	if err != nil {
		slog.Warn(p.tag()+": nex work item callback failed",
			"error", err,
			"work_item_id", body["workItemId"],
			"callback_url", p.ltsWorkItemCallbackURL,
		)
		return
	}
	slog.Warn(p.tag()+": nex work item callback recorded",
		"work_item_id", body["workItemId"],
		"status", parsed.Status,
		"patches", len(parsed.CardPatches),
	)
	p.applyNexWorkItemCardPatches(context.Background(), parsed.CardPatches, clickerMessageID, false)
}

func (p *Platform) postNexWorkItemCallbackSync(body map[string]any) (nexWorkItemCallbackResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nexWorkItemCallbackResponse{}, fmt.Errorf("marshal body: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), nexWorkItemCallbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.ltsWorkItemCallbackURL, bytes.NewReader(payload))
	if err != nil {
		return nexWorkItemCallbackResponse{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(p.ltsWorkItemCallbackAPIKey); key != "" {
		req.Header.Set("X-LTS-API-Key", key)
	}
	client := p.ltsWorkItemCallbackHTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nexWorkItemCallbackResponse{}, err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nexWorkItemCallbackResponse{}, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var parsed nexWorkItemCallbackResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return nexWorkItemCallbackResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return parsed, nil
}

func (p *Platform) applyNexWorkItemCardPatches(ctx context.Context, patches []nexWorkItemCardPatch, clickerMessageID string, skipClicker bool) {
	if p == nil || p.client == nil {
		return
	}
	clickerMessageID = strings.TrimSpace(clickerMessageID)
	for _, patch := range patches {
		messageID := strings.TrimSpace(patch.MessageID)
		if messageID == "" || patch.Card == nil {
			continue
		}
		if skipClicker && messageID == clickerMessageID {
			continue
		}
		if err := p.patchWorkItemCardMap(ctx, messageID, patch.Card); err != nil {
			slog.Warn(p.tag()+": nex work item card patch failed",
				"error", err,
				"message_id", messageID,
			)
		}
	}
}

func (p *Platform) patchWorkItemCardMap(ctx context.Context, messageID string, card map[string]any) error {
	content, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal card: %w", err)
	}
	return p.patchCardMessage(ctx, messageID, string(content))
}

func isNexCallbackValue(value map[string]any) bool {
	if value == nil {
		return false
	}
	v, ok := value["nexCallback"]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	case float64:
		return x != 0
	default:
		return fmt.Sprint(x) == "true"
	}
}

func buildNexOptimisticResultCard(rawTitle, itemType, details, banner, inboxURL string) map[string]any {
	elements := make([]any, 0, 5)
	if strings.TrimSpace(details) != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": details})
		elements = append(elements, map[string]any{"tag": "hr"})
	}
	elements = append(elements, map[string]any{"tag": "markdown", "content": banner})
	if button := nexWorkbenchJumpButton(inboxURL); button != nil {
		elements = append(elements, button)
	}
	title, template := nexCardHeaderForType(itemType, rawTitle)
	if strings.TrimSpace(title) == "" {
		title = "Nex"
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true, "width_mode": "fill"},
		"header": map[string]any{
			"template": template,
			"title":    map[string]any{"tag": "plain_text", "content": title},
		},
		"body": map[string]any{"elements": elements},
	}
}

func nexCardHeaderForType(itemType, rawTitle string) (title, template string) {
	t := strings.TrimSpace(rawTitle)
	switch strings.TrimSpace(itemType) {
	case "review":
		return "待你审核 · " + t, "orange"
	case "decision":
		return "待你决策 · " + t, "orange"
	case "exception":
		return "异常待处置 · " + t, "red"
	case "fyi":
		return "知会 · " + t, "blue"
	default:
		return t, "orange"
	}
}

func nexWorkbenchJumpButton(inboxURL string) map[string]any {
	inboxURL = strings.TrimSpace(inboxURL)
	if inboxURL == "" {
		return nil
	}
	return map[string]any{
		"tag":   "button",
		"text":  map[string]any{"tag": "plain_text", "content": "打开 Workbench"},
		"type":  "default",
		"width": "fill",
		"behaviors": []any{map[string]any{
			"type":        "open_url",
			"default_url": inboxURL,
			"pc_url":      inboxURL,
			"android_url": inboxURL,
			"ios_url":     inboxURL,
		}},
	}
}

func mapStringValue(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func extractNexFormComment(formValue map[string]interface{}, inputValue string) string {
	if len(formValue) > 0 {
		if v, ok := formValue[larkCommentFieldName]; ok {
			if comment := strings.TrimSpace(fmt.Sprint(v)); comment != "" && comment != "<nil>" {
				return comment
			}
		}
	}
	return strings.TrimSpace(inputValue)
}

func resultPresentationForNex(status, label, comment string) (toastType, toastContent, banner string) {
	comment = strings.TrimSpace(comment)
	switch status {
	case "ok":
		toast := "已提交：" + label
		banner = "**你的处理**\n✅ 已选择：" + label
		if comment != "" {
			toast += "（含备注）"
			banner += "\n备注：" + comment
		}
		return "success", toast, banner
	case "already_resolved":
		return "info", "该事项已处理", "**你的处理**\nℹ️ 该事项已处理，无需重复操作。"
	default:
		return "error", "处理失败，请打开 Nex 收件箱", "**你的处理**\n⚠️ 处理失败，请打开 Nex 收件箱操作。"
	}
}

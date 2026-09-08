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

const nexWorkItemCallbackTimeout = 2*time.Second + 500*time.Millisecond

// handleNexWorkItemCardAction forwards Nex work-item card clicks to LTS HTTP and
// returns an immediate toast plus the updated card in the callback response.
func (p *Platform) handleNexWorkItemCardAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool) {
	if event == nil || event.Event == nil || event.Event.Action == nil || event.Event.Action.Value == nil {
		return nil, false
	}
	value := event.Event.Action.Value
	if value["nexCallback"] != true {
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

	parsed, err := p.postNexWorkItemCallbackSync(body)
	status := strings.TrimSpace(parsed.Status)
	if err != nil {
		slog.Error(p.tag()+": nex work item callback failed", "error", err, "work_item_id", workItemID)
		status = "error"
	} else if status == "" {
		status = "ok"
	}

	toastType, toastContent, _ := resultPresentationForNex(status, label, comment)
	resp := &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{Type: toastType, Content: toastContent},
	}
	if clickerCard := patchCardForMessage(parsed.CardPatches, messageID); clickerCard != nil {
		resp.Card = &callback.Card{Type: "raw", Data: clickerCard}
	} else if err == nil {
		slog.Warn(p.tag()+": nex work item callback returned no card patch for clicker",
			"work_item_id", workItemID,
			"message_id", messageID,
			"patches", len(parsed.CardPatches),
		)
	}

	go p.applyNexWorkItemCardPatches(context.Background(), parsed.CardPatches, messageID, resp.Card != nil)

	slog.Info(p.tag()+": nex work item card action handled",
		"work_item_id", workItemID,
		"message_id", messageID,
		"status", status,
		"callback_card", resp.Card != nil,
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

func patchCardForMessage(patches []nexWorkItemCardPatch, messageID string) map[string]any {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil
	}
	for _, patch := range patches {
		if strings.TrimSpace(patch.MessageID) == messageID && patch.Card != nil {
			return patch.Card
		}
	}
	return nil
}

func (p *Platform) applyNexWorkItemCardPatches(ctx context.Context, patches []nexWorkItemCardPatch, clickerMessageID string, clickerUpdatedInCallback bool) {
	clickerMessageID = strings.TrimSpace(clickerMessageID)
	for _, patch := range patches {
		messageID := strings.TrimSpace(patch.MessageID)
		if messageID == "" || patch.Card == nil {
			continue
		}
		if clickerUpdatedInCallback && messageID == clickerMessageID {
			continue
		}
		if err := p.patchWorkItemCardMap(ctx, messageID, patch.Card); err != nil {
			slog.Error(p.tag()+": nex work item card patch failed",
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

func mapStringValue(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func extractNexFormComment(formValue map[string]interface{}, inputValue string) string {
	if len(formValue) > 0 {
		if v, ok := formValue[larkCommentFieldName]; ok {
			if comment := strings.TrimSpace(strings.TrimSpace(fmt.Sprint(v))); comment != "" && comment != "<nil>" {
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

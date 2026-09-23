package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (p *Platform) SetDecisionHandler(h func(string, string, string, string, string) (*core.Decision, error)) {
	p.decisionHandler = h
}
func (p *Platform) ResolveDecisionRecipient(ctx context.Context, recipient string) (string, error) {
	if strings.HasPrefix(recipient, "ou_") && !strings.ContainsAny(recipient, " @,;\n\t") {
		return recipient, nil
	}
	if strings.HasPrefix(recipient, "ou_") || strings.HasPrefix(recipient, "oc_") || strings.HasPrefix(recipient, "cli_") {
		return "", fmt.Errorf("invalid app-scoped recipient; never append an email domain to an open_id")
	}
	a, err := mail.ParseAddress(recipient)
	if err != nil || a.Address != recipient {
		return "", fmt.Errorf("recipient must be a full email address or this app's open_id; do not guess an email from an account name")
	}
	var id string
	err = p.withFreshTenantAccessTokenRetry(ctx, "resolve decision recipient", func(client *lark.Client, opts ...larkcore.RequestOptionFunc) error {
		resp, err := client.Contact.User.BatchGetId(ctx, larkcontact.NewBatchGetIdUserReqBuilder().UserIdType("open_id").Body(larkcontact.NewBatchGetIdUserReqBodyBuilder().Emails([]string{recipient}).IncludeResigned(false).Build()).Build(), opts...)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("recipient lookup code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil || len(resp.Data.UserList) != 1 || resp.Data.UserList[0].UserId == nil {
			return fmt.Errorf("recipient not found in app directory scope")
		}
		id = *resp.Data.UserList[0].UserId
		return nil
	})
	if err == nil && id == "" {
		err = fmt.Errorf("recipient has no open_id")
	}
	return id, err
}

func decisionCard(v *core.Decision, answered bool) map[string]any {
	i := core.NewI18n(core.DetectLanguage(v.Spec.Title + v.Spec.Markdown))
	elements := []map[string]any{{"tag": "markdown", "content": sanitizeMarkdownURLs(preprocessFeishuMarkdown(v.Spec.Markdown))}}
	color := "blue"
	if answered {
		color = "green"
		label := ""
		for _, o := range v.Spec.Options {
			if o.ID == v.OptionID {
				label = o.Label
			}
		}
		elements = append(elements, map[string]any{"tag": "div", "text": plainText(label)})
		if v.Comment != "" {
			elements = append(elements, map[string]any{"tag": "div", "text": plainText(v.Comment)})
		}
		elements = append(elements, map[string]any{"tag": "note", "elements": []any{plainText(i.T(core.MsgDecisionSaved))}})
	} else {
		form := []map[string]any{}
		if v.Spec.AllowComment {
			form = append(form, map[string]any{"tag": "input", "name": "comment", "placeholder": plainText(i.T(core.MsgDecisionComment)), "max_length": 1000, "input_type": "multiline_text"})
		}
		columns := []map[string]any{}
		for idx, o := range v.Spec.Options {
			button := map[string]any{"tag": "button", "name": fmt.Sprintf("decision_%d", idx), "text": plainText(o.Label), "type": "default", "form_action_type": "submit", "value": map[string]string{"action": "decision:submit", "request_id": v.ID, "option_id": o.ID}}
			columns = append(columns, map[string]any{"tag": "column", "width": "auto", "elements": []any{button}})
		}
		form = append(form, map[string]any{"tag": "column_set", "columns": columns})
		elements = append(elements, map[string]any{"tag": "form", "name": "decision_form", "elements": form})
	}
	return map[string]any{"config": map[string]any{"wide_screen_mode": true, "update_multi": true}, "header": map[string]any{"title": plainText(v.Spec.Title), "template": color}, "elements": elements}
}
func (p *Platform) SendDecision(ctx context.Context, v *core.Decision) (string, error) {
	card, err := json.Marshal(decisionCard(v, false))
	if err != nil {
		return "", err
	}
	var id string
	err = p.withFreshTenantAccessTokenRetry(ctx, "send decision card", func(client *lark.Client, opts ...larkcore.RequestOptionFunc) error {
		if v.DeliverySessionKey != "" {
			raw, err := p.ReconstructReplyCtx(v.DeliverySessionKey)
			if err != nil {
				return err
			}
			rc := raw.(replyContext)
			if p.shouldReplyInThread(rc) {
				resp, err := client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().MessageId(rc.messageID).Body(larkim.NewReplyMessageReqBodyBuilder().MsgType("interactive").Content(string(card)).ReplyInThread(true).Uuid(v.ID).Build()).Build(), opts...)
				if err != nil {
					return err
				}
				if !resp.Success() {
					return fmt.Errorf("decision card code=%d msg=%s", resp.Code, resp.Msg)
				}
				if resp.Data == nil || resp.Data.MessageId == nil {
					return fmt.Errorf("decision card missing message ID")
				}
				id = *resp.Data.MessageId
				return nil
			}
			return p.createDecisionMessage(ctx, client, opts, "chat_id", rc.chatID, v.ID, string(card), &id)
		}
		return p.createDecisionMessage(ctx, client, opts, "open_id", v.RecipientID, v.ID, string(card), &id)
	})
	return id, err
}
func (p *Platform) createDecisionMessage(ctx context.Context, client *lark.Client, opts []larkcore.RequestOptionFunc, kind, recipient, uuid, content string, id *string) error {
	resp, err := client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(kind).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(recipient).MsgType("interactive").Content(content).Uuid(uuid).Build()).Build(), opts...)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("decision card code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return fmt.Errorf("decision card missing message ID")
	}
	*id = *resp.Data.MessageId
	return nil
}
func (p *Platform) handleDecisionAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool) {
	if event.Event == nil || event.Event.Action == nil || event.Event.Action.Value["action"] != "decision:submit" {
		return nil, false
	}
	ev := event.Event
	i := core.NewI18n(core.LangChinese)
	rejected := func() (*callback.CardActionTriggerResponse, bool) {
		return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "error", Content: i.T(core.MsgDecisionRejected)}}, true
	}
	if p.decisionHandler == nil || ev.Operator == nil || ev.Context == nil {
		return rejected()
	}
	id, _ := ev.Action.Value["request_id"].(string)
	option, _ := ev.Action.Value["option_id"].(string)
	comment, _ := ev.Action.FormValue["comment"].(string)
	v, err := p.decisionHandler(id, ev.Operator.OpenID, option, comment, ev.Context.OpenMessageID)
	if errors.Is(err, core.ErrDecisionNotFound) {
		return nil, false
	}
	if err != nil {
		return rejected()
	}
	i = core.NewI18n(core.DetectLanguage(v.Spec.Title + v.Spec.Markdown))
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "success", Content: i.T(core.MsgDecisionSaved)}, Card: &callback.Card{Type: "raw", Data: decisionCard(v, true)}}, true
}

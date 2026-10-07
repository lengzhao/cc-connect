package feishu

import (
	"context"
	"fmt"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Read only the triggering topic, never unrelated group history. Missing read
// permission degrades to judging the current message rather than failing routing.
func (p *Platform) jevThreadContext(ctx context.Context, req jevAdmitRequest) string {
	if p.client == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var items []*larkim.Message
	threadID := req.ThreadID
	if req.RootID != "" {
		err := p.withFreshTenantAccessTokenRetry(ctx, "jev topic root", func(client *lark.Client, opts ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.Get(ctx, larkim.NewGetMessageReqBuilder().MessageId(req.RootID).UserIdType("open_id").Build(), opts...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("code=%d", resp.Code)
			}
			if resp.Data != nil {
				items = append(items, resp.Data.Items...)
			}
			return nil
		})
		if err != nil {
			slog.Debug(p.tag()+": jev topic root unavailable", "error", err)
		}
		for _, m := range items {
			if m != nil && threadID == "" {
				threadID = stringValue(m.ThreadId)
			}
		}
	}
	if threadID != "" {
		err := p.withFreshTenantAccessTokenRetry(ctx, "jev topic context", func(client *lark.Client, opts ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.List(ctx, larkim.NewListMessageReqBuilder().ContainerIdType("thread").ContainerId(threadID).SortType("ByCreateTimeDesc").PageSize(8).Build(), opts...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("code=%d", resp.Code)
			}
			if resp.Data != nil {
				items = append(items, resp.Data.Items...)
			}
			return nil
		})
		if err != nil {
			slog.Debug(p.tag()+": jev topic context unavailable", "error", err)
		}
	}
	return formatJevContext(items, req, p.getBotOpenID())
}

func formatJevContext(items []*larkim.Message, req jevAdmitRequest, botID string) string {
	filtered := make([]*larkim.Message, 0, len(items))
	seen := map[string]bool{}
	for _, m := range items {
		if m == nil || m.Body == nil || stringValue(m.MessageId) == "" {
			continue
		}
		id := stringValue(m.MessageId)
		if id == req.MessageID || seen[id] || (m.Deleted != nil && *m.Deleted) {
			continue
		}
		if chat := stringValue(m.ChatId); chat != "" && chat != req.ChannelID {
			continue
		}
		created, _ := strconv.ParseInt(stringValue(m.CreateTime), 10, 64)
		if req.CreatedAtMS > 0 && created > req.CreatedAtMS {
			continue
		}
		seen[id] = true
		filtered = append(filtered, m)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, _ := strconv.ParseInt(stringValue(filtered[i].CreateTime), 10, 64)
		b, _ := strconv.ParseInt(stringValue(filtered[j].CreateTime), 10, 64)
		return a < b
	})
	var lines []string
	for _, m := range filtered {
		text := extractTextForJev(stringValue(m.MsgType), stringValue(m.Body.Content), nil, botID)
		if stringValue(m.MsgType) == "interactive" {
			text = extractInteractiveCardText(stringValue(m.Body.Content))
		}
		for _, mention := range m.Mentions {
			if mention != nil {
				text = strings.ReplaceAll(text, stringValue(mention.Key), "@"+stringValue(mention.Name))
			}
		}
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 600 {
			text = string(runes[:600]) + "…"
		}
		sender := "unknown"
		if m.Sender != nil {
			sender = stringValue(m.Sender.Id)
			if sender == botID || stringValue(m.Sender.SenderType) == "app" {
				sender = "bot:" + sender
			}
		}
		lines = append(lines, sender+": "+text)
	}
	if len(lines) > 9 {
		lines = lines[len(lines)-9:]
	}
	return strings.Join(lines, "\n")
}

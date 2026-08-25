package feishu

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/chenhg5/cc-connect/core"
)

const (
	catchupInterval = 3 * time.Minute
	catchupLookback = 5 * time.Minute
	catchupBuffer   = 30 * time.Second
	catchupPageSize = 50
)

// startCatchupPoller starts a background goroutine that periodically fetches
// recent messages from each chat in p.catchupChats and injects any bot-mentioned
// messages missed by the WebSocket push path.
// Only runs when p.isWSPrimary and p.catchupChats is non-empty.
func (p *Platform) startCatchupPoller(ctx context.Context) {
	if p.catchupChats == "" || !p.isWSPrimary {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.catchupCancel = cancel
	p.mu.Unlock()

	go func() {
		defer cancel()
		ticker := time.NewTicker(catchupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.pollAllCatchupChats(ctx)
			}
		}
	}()
}

func (p *Platform) pollAllCatchupChats(ctx context.Context) {
	for _, chatID := range strings.Split(p.catchupChats, ",") {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			continue
		}
		if err := p.pollChatForMissedMentions(ctx, chatID); err != nil {
			slog.Warn(p.tag()+": catchup: poll error", "chat_id", chatID, "error", err)
		}
	}
}

func (p *Platform) pollChatForMissedMentions(ctx context.Context, chatID string) error {
	botOpenID := p.getBotOpenID()
	if botOpenID == "" {
		return nil
	}

	endTime := time.Now().Add(-catchupBuffer).Unix()
	startTime := time.Now().Add(-catchupLookback).Unix()

	req := larkim.NewListMessageReqBuilder().
		ContainerIdType("chat").
		ContainerId(chatID).
		StartTime(fmt.Sprintf("%d", startTime)).
		EndTime(fmt.Sprintf("%d", endTime)).
		PageSize(catchupPageSize).
		Build()

	var items []*larkim.Message
	if err := p.withFreshTenantAccessTokenRetry(ctx, "catchup list messages",
		func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			resp, err := client.Im.Message.List(ctx, req, options...)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("list messages: code=%d msg=%s", resp.Code, resp.Msg)
			}
			if resp.Data != nil {
				items = resp.Data.Items
			}
			return nil
		}); err != nil {
		return err
	}

	injected := 0
	for _, msg := range items {
		if msg == nil || msg.MessageId == nil || msg.CreateTime == nil {
			continue
		}
		if msg.Deleted != nil && *msg.Deleted {
			continue
		}
		if !isBotMentionedInList(msg.Mentions, botOpenID) {
			continue
		}
		if p.dedup.IsDuplicate(*msg.MessageId) {
			continue
		}
		createMs, _ := strconv.ParseInt(*msg.CreateTime, 10, 64)
		msgTime := time.Unix(createMs/1000, (createMs%1000)*int64(time.Millisecond))
		if core.IsOldMessage(msgTime) {
			continue
		}
		p.injectCatchupMessage(ctx, msg, chatID, createMs)
		injected++
	}

	if injected > 0 {
		slog.Info(p.tag()+": catchup: injected missed messages",
			"chat_id", chatID, "count", injected)
	}
	return nil
}

// isBotMentionedInList checks if the bot is mentioned in a REST API message's
// mention list. The REST Mention.Id field is a *string containing the open_id
// directly, unlike WS EventMessage.Mentions[].Id which is a *UserId struct.
func isBotMentionedInList(mentions []*larkim.Mention, botOpenID string) bool {
	for _, m := range mentions {
		if m != nil && m.Id != nil && *m.Id == botOpenID {
			return true
		}
	}
	return false
}

// injectCatchupMessage dispatches a REST API message through the same
// dispatchMessage path used by WebSocket-delivered messages.
func (p *Platform) injectCatchupMessage(ctx context.Context, msg *larkim.Message, chatID string, createTimeMs int64) {
	msgType := stringValue(msg.MsgType)
	content := ""
	if msg.Body != nil && msg.Body.Content != nil {
		content = *msg.Body.Content
	}
	mentions := convertMentions(msg.Mentions)
	messageID := stringValue(msg.MessageId)
	parentID := stringValue(msg.ParentId)

	userID := ""
	if msg.Sender != nil && msg.Sender.Id != nil {
		userID = *msg.Sender.Id
	}

	chatType := "group"
	eventMsg := &larkim.EventMessage{
		MessageId: msg.MessageId,
		RootId:    msg.RootId,
		ChatType:  &chatType,
	}
	sessionKey := p.makeSessionKey(eventMsg, chatID, userID)
	rctx := replyContext{
		messageID:  messageID,
		chatID:     chatID,
		sessionKey: sessionKey,
	}

	slog.Info(p.tag()+": catchup: injecting missed message",
		"message_id", messageID,
		"chat_id", chatID,
		"msg_type", msgType,
	)

	go p.dispatchMessage(ctx, msgType, content, mentions, messageID, sessionKey, userID, chatID, rctx, parentID, createTimeMs)
}

// convertMentions converts REST API []*larkim.Mention to []*larkim.MentionEvent
// for use in dispatchMessage. REST Mention.Id is a *string (open_id directly);
// MentionEvent.Id is a *UserId with an OpenId field.
func convertMentions(mentions []*larkim.Mention) []*larkim.MentionEvent {
	result := make([]*larkim.MentionEvent, 0, len(mentions))
	for _, m := range mentions {
		if m == nil {
			continue
		}
		var userID *larkim.UserId
		if m.Id != nil {
			openID := *m.Id
			userID = &larkim.UserId{OpenId: &openID}
		}
		result = append(result, &larkim.MentionEvent{
			Key:       m.Key,
			Id:        userID,
			Name:      m.Name,
			TenantKey: m.TenantKey,
		})
	}
	return result
}

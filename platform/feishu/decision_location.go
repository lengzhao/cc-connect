package feishu

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (p *Platform) LocateDecisionMessage(ctx context.Context, messageID string) (core.DecisionMessageLocation, error) {
	location := core.DecisionMessageLocation{Platform: p.Name(), MessageID: messageID}
	// Synthetic continuation IDs must never be queried as Lark messages.
	if !strings.HasPrefix(messageID, "om_") || len(messageID) <= 3 {
		return location, fmt.Errorf("not a platform message ID")
	}
	err := p.withFreshTenantAccessTokenRetry(ctx, "read decision message location", func(client *lark.Client, opts ...larkcore.RequestOptionFunc) error {
		resp, err := client.Im.Message.Get(ctx, larkim.NewGetMessageReqBuilder().MessageId(messageID).Build(), opts...)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("message lookup code=%d", resp.Code)
		}
		if resp.Data == nil || len(resp.Data.Items) != 1 {
			return fmt.Errorf("message lookup requires one exact match")
		}
		item := resp.Data.Items[0]
		if item == nil || item.MessageId == nil || *item.MessageId != messageID || item.ChatId == nil || *item.ChatId == "" {
			return fmt.Errorf("message lookup identity mismatch")
		}
		location.ChatID = *item.ChatId
		if item.ThreadId != nil {
			location.ThreadID = *item.ThreadId
		}
		if item.RootId != nil {
			location.RootID = *item.RootId
		}
		if item.ParentId != nil {
			location.ParentID = *item.ParentId
		}
		return nil
	})
	return location, err
}

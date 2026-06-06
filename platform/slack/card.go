package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
)

const cardActionIDPrefix = "cc_card_"

var cardActionSeq uint64

type cardMessageRef struct {
	channel  string
	ts       string
	threadTS string
}

func nextCardActionID() string {
	return fmt.Sprintf("%s%d", cardActionIDPrefix, atomic.AddUint64(&cardActionSeq, 1))
}

func encodeActionValue(action, sessionKey string, extra map[string]string) string {
	payload := map[string]string{"action": action}
	if sessionKey != "" {
		payload["session_key"] = sessionKey
	}
	for k, v := range extra {
		if k == "" || v == "" {
			continue
		}
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		slog.Error("slack: encode action value failed", "error", err)
		return action
	}
	return string(b)
}

func decodeActionValue(raw string) (action, sessionKey string, extra map[string]string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return raw, "", nil
	}
	action = payload["action"]
	sessionKey = payload["session_key"]
	extra = make(map[string]string, len(payload))
	for k, v := range payload {
		if k == "action" || k == "session_key" {
			continue
		}
		extra[k] = v
	}
	if len(extra) == 0 {
		extra = nil
	}
	return action, sessionKey, extra
}

func slackButtonStyle(btnType string) slack.Style {
	switch btnType {
	case "primary":
		return slack.StylePrimary
	case "danger":
		return slack.StyleDanger
	default:
		return ""
	}
}

func plainTextBlock(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.PlainTextType, text, true, false)
}

func mrkdwnBlock(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.MarkdownType, core.MarkdownToSlackMrkdwn(text), false, false)
}

func newButtonElement(text, btnType, action, sessionKey string, extra map[string]string) *slack.ButtonBlockElement {
	btn := slack.NewButtonBlockElement(
		nextCardActionID(),
		encodeActionValue(action, sessionKey, extra),
		plainTextBlock(text),
	)
	if style := slackButtonStyle(btnType); style != "" {
		btn.WithStyle(style)
	}
	return btn
}

func renderCardBlocks(card *core.Card, sessionKey string) []slack.Block {
	if card == nil {
		return []slack.Block{
			slack.NewSectionBlock(mrkdwnBlock(" "), nil, nil),
		}
	}

	var blocks []slack.Block
	if card.Header != nil && card.Header.Title != "" {
		blocks = append(blocks, slack.NewHeaderBlock(plainTextBlock(card.Header.Title)))
	}

	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case core.CardMarkdown:
			if strings.TrimSpace(e.Content) == "" {
				continue
			}
			blocks = append(blocks, slack.NewSectionBlock(mrkdwnBlock(e.Content), nil, nil))
		case core.CardDivider:
			blocks = append(blocks, slack.NewDividerBlock())
		case core.CardActions:
			appendActionBlock(&blocks, e, sessionKey)
		case core.CardListItem:
			btnType := e.BtnType
			if btnType == "" {
				btnType = "default"
			}
			accessory := slack.NewAccessory(newButtonElement(e.BtnText, btnType, e.BtnValue, sessionKey, e.Extra))
			blocks = append(blocks, slack.NewSectionBlock(mrkdwnBlock(e.Text), nil, accessory))
		case core.CardSelect:
			if len(e.Options) == 0 {
				continue
			}
			placeholder := e.Placeholder
			if placeholder == "" {
				placeholder = "Select"
			}
			options := make([]*slack.OptionBlockObject, 0, len(e.Options))
			for _, opt := range e.Options {
				options = append(options, slack.NewOptionBlockObject(
					encodeActionValue(opt.Value, sessionKey, nil),
					plainTextBlock(opt.Text),
					nil,
				))
			}
			selectElem := slack.NewOptionsSelectBlockElement(
				slack.OptTypeStatic,
				plainTextBlock(placeholder),
				nextCardActionID(),
				options...,
			)
			if e.InitValue != "" {
				for _, opt := range e.Options {
					if opt.Value == e.InitValue {
						selectElem = selectElem.WithInitialOption(slack.NewOptionBlockObject(
							encodeActionValue(opt.Value, sessionKey, nil),
							plainTextBlock(opt.Text),
							nil,
						))
						break
					}
				}
			}
			blocks = append(blocks, slack.NewActionBlock("", selectElem))
		case core.CardNote:
			if strings.TrimSpace(e.Text) == "" {
				continue
			}
			blocks = append(blocks, slack.NewContextBlock("", plainTextBlock(e.Text)))
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, slack.NewSectionBlock(mrkdwnBlock(" "), nil, nil))
	}
	return blocks
}

func appendActionBlock(blocks *[]slack.Block, row core.CardActions, sessionKey string) {
	if len(row.Buttons) == 0 {
		return
	}
	if row.Layout == core.CardActionLayoutEqualColumns {
		for i := 0; i < len(row.Buttons); i += 2 {
			end := i + 2
			if end > len(row.Buttons) {
				end = len(row.Buttons)
			}
			chunk := row.Buttons[i:end]
			elements := make([]slack.BlockElement, 0, len(chunk))
			for _, btn := range chunk {
				btnType := btn.Type
				if btnType == "" {
					btnType = "default"
				}
				elements = append(elements, newButtonElement(btn.Text, btnType, btn.Value, sessionKey, btn.Extra))
			}
			*blocks = append(*blocks, slack.NewActionBlock("", elements...))
		}
		return
	}

	const maxButtonsPerRow = 5
	for i := 0; i < len(row.Buttons); i += maxButtonsPerRow {
		end := i + maxButtonsPerRow
		if end > len(row.Buttons) {
			end = len(row.Buttons)
		}
		chunk := row.Buttons[i:end]
		elements := make([]slack.BlockElement, 0, len(chunk))
		for _, btn := range chunk {
			btnType := btn.Type
			if btnType == "" {
				btnType = "default"
			}
			elements = append(elements, newButtonElement(btn.Text, btnType, btn.Value, sessionKey, btn.Extra))
		}
		*blocks = append(*blocks, slack.NewActionBlock("", elements...))
	}
}

func cardFallbackText(card *core.Card) string {
	if card == nil {
		return " "
	}
	if card.Header != nil && card.Header.Title != "" {
		return card.Header.Title
	}
	return strings.TrimSpace(card.RenderText())
}

func (p *Platform) trackCardMessage(sessionKey, channel, ts, threadTS string) {
	if sessionKey == "" || channel == "" || ts == "" {
		return
	}
	p.cardMsgMu.Lock()
	if p.cardMsgRefs == nil {
		p.cardMsgRefs = make(map[string]cardMessageRef)
	}
	p.cardMsgRefs[sessionKey] = cardMessageRef{
		channel:  channel,
		ts:       ts,
		threadTS: threadTS,
	}
	p.cardMsgMu.Unlock()
}

func (p *Platform) postCard(ctx context.Context, rc replyContext, card *core.Card) (string, error) {
	sessionKey := rc.sessionKey
	blocks := renderCardBlocks(card, sessionKey)
	opts := []slack.MsgOption{
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(cardFallbackText(card), false),
	}
	if rc.timestamp != "" {
		opts = append(opts, slack.MsgOptionPostMessageParameters(slack.PostMessageParameters{
			ThreadTimestamp: rc.timestamp,
		}))
	}
	_, ts, err := p.client.PostMessageContext(ctx, rc.channel, opts...)
	if err != nil {
		return "", fmt.Errorf("slack: post card: %w", err)
	}
	p.trackCardMessage(sessionKey, rc.channel, ts, rc.timestamp)
	return ts, nil
}

func (p *Platform) updateCardMessage(ctx context.Context, ref cardMessageRef, card *core.Card, sessionKey string) error {
	blocks := renderCardBlocks(card, sessionKey)
	_, _, _, err := p.client.UpdateMessageContext(
		ctx,
		ref.channel,
		ref.ts,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(cardFallbackText(card), false),
	)
	if err != nil {
		return fmt.Errorf("slack: update card: %w", err)
	}
	return nil
}

// ReplyCard sends a structured card as a threaded or channel reply.
func (p *Platform) ReplyCard(ctx context.Context, rctx any, card *core.Card) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: invalid reply context type %T", rctx)
	}
	_, err := p.postCard(ctx, rc, card)
	return err
}

// SendCard sends a structured card as a new message.
func (p *Platform) SendCard(ctx context.Context, rctx any, card *core.Card) error {
	return p.ReplyCard(ctx, rctx, card)
}

// RefreshCard updates a previously rendered card in-place.
func (p *Platform) RefreshCard(ctx context.Context, sessionKey string, card *core.Card) error {
	p.cardMsgMu.Lock()
	ref, ok := p.cardMsgRefs[sessionKey]
	p.cardMsgMu.Unlock()
	if !ok || ref.channel == "" || ref.ts == "" {
		return fmt.Errorf("slack: no tracked card message for session %q", sessionKey)
	}
	return p.updateCardMessage(ctx, ref, card, sessionKey)
}

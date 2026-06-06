package slack

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

const cardNavTimeout = 2500 * time.Millisecond

// SetCardNavigationHandler registers the engine callback for in-place card updates.
func (p *Platform) SetCardNavigationHandler(h core.CardNavigationHandler) {
	p.cardNavHandler = h
}

func (p *Platform) handleInteractive(evt socketmode.Event) {
	callback, ok := evt.Data.(slack.InteractionCallback)
	if !ok {
		slog.Debug("slack: interactive type assertion failed")
		return
	}
	if evt.Request != nil {
		defer func() {
			if payload := p.buildInteractiveAckPayload(callback); payload != nil {
				p.socket.Ack(*evt.Request, payload)
				return
			}
			p.socket.Ack(*evt.Request)
		}()
	}

	if callback.Type != slack.InteractionTypeBlockActions {
		return
	}
	if !core.AllowList(p.allowFrom, callback.User.ID) {
		slog.Debug("slack: interactive from unauthorized user", "user", callback.User.ID)
		return
	}

	for _, action := range callback.ActionCallback.BlockActions {
		p.handleBlockAction(callback, action)
	}
}

func (p *Platform) buildInteractiveAckPayload(callback slack.InteractionCallback) any {
	p.interactiveAckMu.Lock()
	payload := p.interactiveAckPayload
	p.interactiveAckPayload = nil
	p.interactiveAckMu.Unlock()
	if payload == nil {
		return nil
	}
	return payload
}

func (p *Platform) setInteractiveAckPayload(payload any) {
	p.interactiveAckMu.Lock()
	p.interactiveAckPayload = payload
	p.interactiveAckMu.Unlock()
}

func (p *Platform) handleBlockAction(callback slack.InteractionCallback, action *slack.BlockAction) {
	if action == nil {
		return
	}

	actionVal := strings.TrimSpace(action.Value)
	if actionVal == "" && action.SelectedOption.Value != "" {
		actionVal = action.SelectedOption.Value
	}
	if actionVal == "" {
		return
	}

	actionVal, sessionKey, lang, _ := decodeActionValue(actionVal)
	if sessionKey == "" {
		sessionKey = p.sessionKeyFromInteractive(callback)
	}
	if lang == "" {
		lang = p.langFromActionValue(sessionKey, action.Value)
	} else {
		p.setSessionLang(sessionKey, lang)
	}
	if lang == "" {
		if _, userLang := p.cachedUserInfo(callback.User.ID); userLang != "" {
			lang = userLang
			p.setSessionLang(sessionKey, lang)
		}
	}

	channelID := callback.Channel.ID
	messageTS := callback.Message.Timestamp
	if messageTS == "" {
		messageTS = callback.MessageTs
	}
	threadTS := callback.Message.ThreadTimestamp
	if threadTS == "" {
		threadTS = callback.Container.ThreadTs
	}

	if messageTS != "" {
		p.trackCardMessage(sessionKey, channelID, messageTS, threadTS)
	}

	if strings.HasPrefix(actionVal, "nav:") || strings.HasPrefix(actionVal, "act:") {
		p.handleNavOrActAction(callback, actionVal, sessionKey, lang, channelID, messageTS, threadTS)
		return
	}
	if strings.HasPrefix(actionVal, "perm:") {
		p.handlePermAction(callback, actionVal, sessionKey, lang, channelID, messageTS, threadTS, action.Value)
		return
	}
	if strings.HasPrefix(actionVal, "askq:") {
		p.handleAskQAction(callback, actionVal, sessionKey, lang, channelID, messageTS, threadTS, action.Value)
		return
	}
	if strings.HasPrefix(actionVal, "cmd:") {
		p.handleCmdAction(callback, actionVal, sessionKey, channelID, threadTS)
	}
}

func (p *Platform) sessionKeyFromInteractive(callback slack.InteractionCallback) string {
	threadTS := callback.Message.ThreadTimestamp
	if threadTS == "" {
		threadTS = callback.Container.ThreadTs
	}
	return p.buildSessionKey(callback.Channel.ID, callback.User.ID, threadTS)
}

func (p *Platform) handleNavOrActAction(
	callback slack.InteractionCallback,
	actionVal, sessionKey, lang, channelID, messageTS, threadTS string,
) {
	if p.cardNavHandler == nil {
		return
	}
	p.syncLangFromCardAction(sessionKey, actionVal)

	done := make(chan *core.Card, 1)
	go func() {
		done <- p.cardNavHandler(actionVal, sessionKey)
	}()

	select {
	case card := <-done:
		if card == nil {
			return
		}
		ref := cardMessageRef{channel: channelID, ts: messageTS, threadTS: threadTS}
		if err := p.updateCardMessage(context.Background(), ref, card, sessionKey, lang); err != nil {
			slog.Warn("slack: card action update failed", "action", actionVal, "error", err)
			return
		}
		p.trackCardMessage(sessionKey, channelID, messageTS, threadTS)
	case <-time.After(cardNavTimeout):
		go func() {
			card := <-done
			if card == nil {
				return
			}
			if err := p.RefreshCard(context.Background(), sessionKey, card); err != nil {
				slog.Warn("slack: async card refresh failed", "action", actionVal, "error", err)
			}
		}()
		p.setInteractiveAckPayload(map[string]any{
			"response_type": "ephemeral",
			"text":          slackCardLoadingText(lang),
		})
	}
}

func (p *Platform) handlePermAction(
	callback slack.InteractionCallback,
	actionVal, sessionKey, lang, channelID, messageTS, threadTS, rawValue string,
) {
	var responseText string
	switch actionVal {
	case "perm:allow":
		responseText = "allow"
	case "perm:deny":
		responseText = "deny"
	case "perm:allow_all":
		responseText = "allow all"
	default:
		return
	}

	_, _, _, extra := decodeActionValue(rawValue)
	rctx := replyContext{
		channel:    channelID,
		timestamp:  threadTS,
		sessionKey: sessionKey,
		lang:       lang,
	}
	if p.handler != nil {
		go p.handler(p, &core.Message{
			SessionKey: sessionKey,
			Platform:   "slack",
			UserID:     callback.User.ID,
			UserName:   p.resolveUserName(callback.User.ID),
			ChatName:   p.resolveChannelNameForMsg(channelID),
			Content:    responseText,
			ReplyCtx:   rctx,
		})
	}

	permLabel := extra["perm_label"]
	permBody := extra["perm_body"]
	if permLabel == "" {
		permLabel = "✅ " + responseText
	}
	cb := core.NewCard().Title(permLabel, "green")
	if permBody != "" {
		cb.Markdown(permBody)
	}
	card := cb.Build()
	ref := cardMessageRef{channel: channelID, ts: messageTS, threadTS: threadTS}
	if err := p.updateCardMessage(context.Background(), ref, card, sessionKey, lang); err != nil {
		slog.Warn("slack: perm card update failed", "error", err)
	}
}

func (p *Platform) handleAskQAction(
	callback slack.InteractionCallback,
	actionVal, sessionKey, lang, channelID, messageTS, threadTS, rawValue string,
) {
	_, _, _, extra := decodeActionValue(rawValue)
	rctx := replyContext{
		channel:    channelID,
		timestamp:  threadTS,
		sessionKey: sessionKey,
		lang:       lang,
	}
	if p.handler != nil {
		go p.handler(p, &core.Message{
			SessionKey: sessionKey,
			Platform:   "slack",
			UserID:     callback.User.ID,
			UserName:   p.resolveUserName(callback.User.ID),
			ChatName:   p.resolveChannelNameForMsg(channelID),
			Content:    actionVal,
			ReplyCtx:   rctx,
		})
	}

	answerLabel := extra["askq_label"]
	question := extra["askq_question"]
	if answerLabel == "" {
		answerLabel = actionVal
	}
	selected := slackAskQuestionSelectedText(lang)
	cb := core.NewCard().Title("✅ "+answerLabel, "green")
	if question != "" {
		cb.Markdown(question)
	}
	cb.Markdown("**" + selected + " → " + answerLabel + "**")
	card := cb.Build()
	ref := cardMessageRef{channel: channelID, ts: messageTS, threadTS: threadTS}
	if err := p.updateCardMessage(context.Background(), ref, card, sessionKey, lang); err != nil {
		slog.Warn("slack: askq card update failed", "error", err)
	}
}

func (p *Platform) handleCmdAction(
	callback slack.InteractionCallback,
	actionVal, sessionKey, channelID, threadTS string,
) {
	cmdText := strings.TrimPrefix(actionVal, "cmd:")
	if cmdText == "" || p.handler == nil {
		return
	}
	if !strings.HasPrefix(cmdText, "/") {
		cmdText = "/" + cmdText
	}
	rctx := replyContext{
		channel:    channelID,
		timestamp:  threadTS,
		sessionKey: sessionKey,
		lang:       p.langForSession(sessionKey),
	}
	slog.Info("slack: card action dispatched as command", "cmd", cmdText, "user", callback.User.ID)
	go p.handler(p, &core.Message{
		SessionKey: sessionKey,
		Platform:   "slack",
		UserID:     callback.User.ID,
		UserName:   p.resolveUserName(callback.User.ID),
		ChatName:   p.resolveChannelNameForMsg(channelID),
		Content:    cmdText,
		ReplyCtx:   rctx,
	})
}

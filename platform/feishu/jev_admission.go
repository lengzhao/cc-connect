package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// jevChatAllowlist mirrors agent-runtime/jev.ParseChatAllowlist:
// unset/empty → OFF; "all" → all groups; comma-separated oc_… → those chats.
type jevChatAllowlist struct {
	on  bool
	all bool
	ids map[string]struct{}
}

func parseJevChatAllowlist(raw string) jevChatAllowlist {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return jevChatAllowlist{}
	}
	if strings.EqualFold(raw, "all") {
		return jevChatAllowlist{on: true, all: true}
	}
	ids := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	if len(ids) == 0 {
		return jevChatAllowlist{}
	}
	return jevChatAllowlist{on: true, ids: ids}
}

func (a jevChatAllowlist) matches(chatID string) bool {
	if !a.on {
		return false
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return false
	}
	if a.all {
		return true
	}
	_, ok := a.ids[chatID]
	return ok
}

type jevAdmitRequest struct {
	Message   string `json:"message"`
	Channel   string `json:"channel,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	Sender    string `json:"sender,omitempty"`
}

type jevAdmitResponse struct {
	Admitted bool   `json:"admitted"`
	Reason   string `json:"reason"`
}

// callJevAdmission POSTs to the Runtime local admission URL.
// Fail-safe: any transport/decode problem → not admitted (stay quiet).
func callJevAdmission(ctx context.Context, client *http.Client, url, message, channelID, channelName, sender string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		slog.Warn("feishu: jev admission url unset; stay quiet")
		return false
	}
	body, err := json.Marshal(jevAdmitRequest{
		Message:   message,
		Channel:   channelName,
		ChannelID: channelID,
		Sender:    sender,
	})
	if err != nil {
		slog.Warn("feishu: jev admission marshal failed; stay quiet", "err", err)
		return false
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("feishu: jev admission request build failed; stay quiet", "err", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("feishu: jev admission request failed; stay quiet", "err", err, "url", url)
		return false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		slog.Warn("feishu: jev admission read failed; stay quiet", "err", err)
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("feishu: jev admission http error; stay quiet", "status", resp.StatusCode, "body", string(raw))
		return false
	}
	var out jevAdmitResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		slog.Warn("feishu: jev admission decode failed; stay quiet", "err", err)
		return false
	}
	slog.Info("feishu: jev admission result",
		"admitted", out.Admitted,
		"reason", out.Reason,
		"chat_id", channelID,
	)
	return out.Admitted
}

func jevEngagedRootKey(chatID, rootID string) string {
	return strings.TrimSpace(chatID) + ":" + strings.TrimSpace(rootID)
}

// markJevThreadEngaged records that the bot was activated (@) under this topic root.
func (p *Platform) markJevThreadEngaged(chatID, rootID string) {
	chatID = strings.TrimSpace(chatID)
	rootID = strings.TrimSpace(rootID)
	if chatID == "" || rootID == "" {
		return
	}
	p.jevEngagedRoots.Store(jevEngagedRootKey(chatID, rootID), time.Now())
}

// isJevThreadEngaged reports whether this topic was previously activated by @bot.
// Follow-ups carry root_id of the activating message; thread_id alone is not enough.
func (p *Platform) isJevThreadEngaged(chatID, rootID, threadID string) bool {
	chatID = strings.TrimSpace(chatID)
	rootID = strings.TrimSpace(rootID)
	if chatID == "" {
		return false
	}
	if rootID != "" {
		if _, ok := p.jevEngagedRoots.Load(jevEngagedRootKey(chatID, rootID)); ok {
			return true
		}
	}
	// Some Feishu deliveries put the activating message id in thread_id.
	threadID = strings.TrimSpace(threadID)
	if threadID != "" {
		if _, ok := p.jevEngagedRoots.Load(jevEngagedRootKey(chatID, threadID)); ok {
			return true
		}
	}
	return false
}

// admitUnmentionedGroup runs sync HTTP admission against Runtime.
// Returns true only when Jev explicitly admits the message.
func (p *Platform) admitUnmentionedGroup(ctx context.Context, msgType, content string, mentions []*larkim.MentionEvent, chatID, userID string) bool {
	text := extractTextForJev(msgType, content, mentions, p.getBotOpenID())
	if text == "" {
		slog.Debug(p.tag()+": jev admission skip — empty extractable text; stay quiet",
			"chat_id", chatID, "msg_type", msgType)
		return false
	}
	// Keep this synchronous and bounded so onMessage can drop before dispatch.
	return callJevAdmission(ctx, p.jevAdmissionHTTP, p.jevAdmissionURL, text, chatID, "", userID)
}

// extractTextForJev returns text suitable for admission. Empty → caller stays quiet.
func extractTextForJev(msgType, content string, mentions []*larkim.MentionEvent, botOpenID string) string {
	switch msgType {
	case "text":
		var textBody struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &textBody); err != nil {
			return ""
		}
		return strings.TrimSpace(stripMentions(textBody.Text, mentions, botOpenID))
	case "post":
		return strings.TrimSpace(stripMentions(extractPostPlainText(content), mentions, botOpenID))
	default:
		return ""
	}
}

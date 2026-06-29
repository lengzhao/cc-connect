package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func init() {
	core.RegisterPlatform("slack", New)
}

type replyContext struct {
	channel          string
	timestamp        string // thread_ts for threading replies
	messageTS        string // triggering user message ts (for emoji reactions)
	sessionKey       string
	lang             string // normalized UI language for this session
	slackMentions    []slackMentionRef
	slashResponseURL string // slash command delayed-response URL
}

type slackMentionRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type Platform struct {
	botToken              string
	appToken              string
	apiURL                string
	allowFrom             string
	sessionScope          string // "user" (default) | "channel" | "thread"
	reactionEmoji         string // emoji on incoming message (default eyes)
	doneEmoji             string // emoji when processing completes (default white_check_mark)
	injectMentionedUsers  bool   // prepend Slack mention metadata for users mentioned in inbound text
	includeUserEmail      bool   // include Slack profile email in injected metadata when available
	groupReplyAll              bool // respond to all channel messages without @mention (default false)
	threadReplyWithoutMention  bool // allow follow-ups in bot-engaged threads without @mention (default true)
	statePath                  string
	threadActiveTTL            time.Duration
	dedupTTL                   time.Duration
	progressStyle              string
	client                     *slack.Client
	socket                *socketmode.Client
	handler               core.MessageHandler
	self                  core.Platform
	statusKeepaliveMu     sync.Mutex
	statusKeepalive       map[string]context.CancelFunc
	cardNavHandler        core.CardNavigationHandler
	cancel                context.CancelFunc
	channelNameCache      map[string]string
	channelCacheMu        sync.RWMutex
	userInfoCache         sync.Map // userID -> slackUserInfo (display name + Slack locale)
	sessionLang    sync.Map // sessionKey -> normalized language code
	threadStateMu  sync.Mutex
	activeThreads  map[string]time.Time // channel:thread_ts -> marked at (bot-engaged threads)
	inboundDedupMu sync.Mutex
	recentInbound  map[string]time.Time // channel:msg_ts -> seen at (dedupe app_mention vs message)
	cardMsgMu      sync.Mutex
	cardMsgRefs           map[string]cardMessageRef
	interactiveAckMu      sync.Mutex
	interactiveAckPayload any
}

func New(opts map[string]any) (core.Platform, error) {
	botToken, _ := opts["bot_token"].(string)
	appToken, _ := opts["app_token"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("slack", allowFrom)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	apiURL, err := normalizeSlackAPIURL(opts)
	if err != nil {
		return nil, err
	}
	if botToken == "" || appToken == "" {
		return nil, fmt.Errorf("slack: bot_token and app_token are required")
	}
	scope := normalizeSessionScope(opts["session_scope"], shareSessionInChannel)
	if scope == "thread" {
		slog.Warn("slack: session_scope=thread gives each Slack thread its own session; " +
			"if your agent runtime is tmux, also set window_per_session=true — " +
			"without it, concurrent threads share a single pane and their output will interleave")
	}
	reactionEmoji, _ := opts["reaction_emoji"].(string)
	if reactionEmoji == "" {
		reactionEmoji = "eyes"
	}
	if v, ok := opts["reaction_emoji"].(string); ok && v == "none" {
		reactionEmoji = ""
	}
	doneEmoji, _ := opts["done_emoji"].(string)
	if doneEmoji == "" {
		doneEmoji = "white_check_mark"
	}
	if v, ok := opts["done_emoji"].(string); ok && v == "none" {
		doneEmoji = ""
	}
	injectMentionedUsers := true
	if v, ok := opts["inject_mentioned_users"].(bool); ok {
		injectMentionedUsers = v
	}
	includeUserEmail, _ := opts["include_user_email"].(bool)
	groupReplyAll, _ := opts["group_reply_all"].(bool)
	// require_mention = false is equivalent to group_reply_all = true:
	// both mean "respond to all channel messages without needing an @mention".
	if v, ok := opts["require_mention"].(bool); ok && !v {
		groupReplyAll = true
	}
	threadReplyWithoutMention := true
	if v, ok := opts["thread_reply_without_mention"].(bool); ok {
		threadReplyWithoutMention = v
	}
	threadActiveHours := parsePositiveIntOpt(opts["thread_active_ttl_hours"], defaultThreadActiveHours)
	dedupSecs := parsePositiveIntOpt(opts["dedup_ttl_seconds"], defaultInboundDedupSecs)
	progressStyle, err := parseSlackProgressStyle(opts)
	if err != nil {
		return nil, err
	}
	p := &Platform{
		botToken:                  botToken,
		appToken:                  appToken,
		apiURL:                    apiURL,
		allowFrom:                 allowFrom,
		sessionScope:              scope,
		reactionEmoji:             reactionEmoji,
		doneEmoji:                 doneEmoji,
		injectMentionedUsers:      injectMentionedUsers,
		includeUserEmail:          includeUserEmail,
		groupReplyAll:             groupReplyAll,
		threadReplyWithoutMention: threadReplyWithoutMention,
		statePath:                 resolveSlackStatePath(opts),
		threadActiveTTL:           time.Duration(threadActiveHours) * time.Hour,
		dedupTTL:                  time.Duration(dedupSecs) * time.Second,
		progressStyle:             progressStyle,
		activeThreads:             make(map[string]time.Time),
		recentInbound:             make(map[string]time.Time),
		channelNameCache:          make(map[string]string),
		statusKeepalive:           make(map[string]context.CancelFunc),
	}
	p.loadSlackThreadState()
	if progressStyle == progressStyleAssistantStatus || progressStyle == progressStyleStream {
		wrapped := &progressPlatform{Platform: p}
		p.self = wrapped
		return wrapped, nil
	}
	p.self = p
	return p, nil
}

func parseSlackProgressStyle(opts map[string]any) (string, error) {
	raw, _ := opts["progress_style"].(string)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "legacy":
		return "legacy", nil
	case progressStyleAssistantStatus:
		return progressStyleAssistantStatus, nil
	case progressStyleStream:
		return progressStyleStream, nil
	default:
		return "", fmt.Errorf("slack: invalid progress_style %q (want legacy, assistant_status, or stream)", raw)
	}
}

func (p *Platform) selfPlatform() core.Platform {
	if p != nil && p.self != nil {
		return p.self
	}
	return p
}

// normalizeSlackAPIURL returns the Slack Web API base URL in the format
// expected by slack-go. Custom endpoints may be specified as a host or /api URL.
func normalizeSlackAPIURL(opts map[string]any) (string, error) {
	raw, _ := opts["api_url"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return slack.APIURL, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return "", fmt.Errorf("slack: invalid api_url %q: must be a valid http(s) URL", raw)
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	if path == "" || !strings.HasSuffix(path, "/api") {
		if path == "" {
			path = "/api"
		} else {
			path += "/api"
		}
	}
	parsed.Path = path + "/"
	return parsed.String(), nil
}

// normalizeSessionScope resolves the configured session_scope option to one of
// "user" | "channel" | "thread". For backward compatibility, when session_scope
// is unset, share_session_in_channel = true maps to "channel"; otherwise the
// default is "user". Unknown values fall back to the share-derived default.
func normalizeSessionScope(raw any, shareInChannel bool) string {
	fallback := "user"
	if shareInChannel {
		fallback = "channel"
	}
	s, _ := raw.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "user":
		return "user"
	case "channel":
		return "channel"
	case "thread":
		return "thread"
	case "":
		return fallback
	default:
		slog.Warn("slack: unknown session_scope, falling back", "session_scope", s, "using", fallback)
		return fallback
	}
}

// buildSessionKey derives the engine session key for an incoming Slack event
// according to the configured session_scope:
//   - "channel": one session per channel            -> slack:<channel>
//   - "thread":  one session per thread (parent ts)  -> slack:<channel>:t:<threadTS>
//   - "user":    one session per (channel, user)     -> slack:<channel>:<user>  (default)
//
// threadTS must be the thread root timestamp; pass "" when no thread context is
// available (e.g. slash commands), in which case "thread" falls back to "user".
func (p *Platform) buildSessionKey(channel, user, threadTS string) string {
	switch p.sessionScope {
	case "channel":
		return fmt.Sprintf("slack:%s", channel)
	case "thread":
		if threadTS != "" {
			return fmt.Sprintf("slack:%s:t:%s", channel, threadTS)
		}
		return fmt.Sprintf("slack:%s:%s", channel, user)
	default:
		return fmt.Sprintf("slack:%s:%s", channel, user)
	}
}

// threadRootTS returns the thread parent timestamp for an event: the existing
// thread_ts when the message is already in a thread, otherwise the message's
// own ts (which becomes the thread root once the bot replies in-thread).
func threadRootTS(threadTS, msgTS string) string {
	if threadTS != "" {
		return threadTS
	}
	return msgTS
}

func (p *Platform) Name() string { return "slack" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler

	clientOpts := []slack.Option{
		slack.OptionAppLevelToken(p.appToken),
	}
	if p.apiURL != slack.APIURL {
		clientOpts = append(clientOpts, slack.OptionAPIURL(p.apiURL))
		slog.Info("slack: using custom API URL", "api_url", p.apiURL)
	}
	p.client = slack.New(p.botToken, clientOpts...)
	p.socket = socketmode.New(p.client)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-p.socket.Events:
				p.handleEvent(evt)
			}
		}
	}()

	go func() {
		if err := p.socket.RunContext(ctx); err != nil {
			slog.Error("slack: socket mode error", "error", err)
		}
	}()

	slog.Info("slack: socket mode connected")
	return nil
}

func (p *Platform) handleEvent(evt socketmode.Event) {
	slog.Debug("slack: raw event received", "type", evt.Type)
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		data, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			slog.Debug("slack: EventsAPI type assertion failed")
			return
		}
		slog.Debug("slack: EventsAPI event", "outer_type", data.Type, "inner_type", data.InnerEvent.Type)
		if evt.Request != nil {
			p.socket.Ack(*evt.Request)
		}

		if data.Type == slackevents.CallbackEvent {
			switch ev := data.InnerEvent.Data.(type) {
			case *slackevents.AppMentionEvent:
				if ev.BotID != "" || ev.User == "" {
					return
				}

				if ts := ev.TimeStamp; ts != "" {
					if dotIdx := strings.IndexByte(ts, '.'); dotIdx > 0 {
						if sec, err := strconv.ParseInt(ts[:dotIdx], 10, 64); err == nil {
							if core.IsOldMessage(time.Unix(sec, 0)) {
								slog.Debug("slack: ignoring old app_mention after restart", "ts", ts)
								return
							}
						}
					}
				}

				slog.Debug("slack: app_mention received", "user", ev.User, "channel", ev.Channel)

				if !core.AllowList(p.allowFrom, ev.User) {
					slog.Debug("slack: app_mention from unauthorized user", "user", ev.User)
					return
				}

				if p.isDuplicateInbound(ev.Channel, ev.TimeStamp) {
					slog.Debug("slack: duplicate app_mention skipped", "channel", ev.Channel, "ts", ev.TimeStamp)
					return
				}

				threadTS := threadRootTS(ev.ThreadTimeStamp, ev.TimeStamp)
				p.markActiveThread(ev.Channel, threadTS)
				sessionKey := p.buildSessionKey(ev.Channel, ev.User, threadTS)

				var shareFiles []slackevents.File
				if cb, ok := data.Data.(*slackevents.EventsAPICallbackEvent); ok {
					shareFiles = parseSlackInnerEventFiles(cb.InnerEvent)
				}
				images, audio, docFiles := p.processSlackFileShares(shareFiles)
				content := stripAppMentionText(ev.Text)
				if content == "" && len(images) == 0 && audio == nil && len(docFiles) == 0 {
					return
				}
				lang := p.rememberSessionLang(sessionKey, ev.User, content)
				mentions := p.resolveMentionedUsers(content)
				rc := replyContext{
					channel: ev.Channel, timestamp: threadTS,
					messageTS: ev.TimeStamp, sessionKey: sessionKey, lang: lang,
					slackMentions: mentions,
				}
				p.reactReceived(rc)
				userName, userEmail := p.resolveUserNameAndEmail(ev.User)
				msg := &core.Message{
					SessionKey: sessionKey, Platform: "slack",
					UserID: ev.User, UserName: userName, UserEmail: userEmail,
					ChatName:     p.resolveChannelNameForMsg(ev.Channel),
					Content:      content,
					ExtraContent: formatMentionExtraContent(mentions),
					Images:       images,
					Files:        docFiles,
					Audio:        audio,
					MessageID:    ev.TimeStamp,
					ReplyCtx:     rc,
				}
				p.handler(p.selfPlatform(), msg)

			case *slackevents.AssistantThreadStartedEvent:
				// User opened a Slack Assistant Chat thread for this app.
				// Subsequent messages arrive with ThreadTimeStamp set;
				// assistantOrThreadTS() routes replies into that thread (Chat tab UI).
				slog.Info("slack: assistant_thread_started",
					"user", ev.AssistantThread.UserID,
					"channel", ev.AssistantThread.ChannelID,
					"thread_ts", ev.AssistantThread.ThreadTimeStamp)
				_ = p.client.SetAssistantThreadsStatus(slack.AssistantThreadsSetStatusParameters{
					ChannelID: ev.AssistantThread.ChannelID,
					ThreadTS:  ev.AssistantThread.ThreadTimeStamp,
					Status:    "",
				})

			case *slackevents.MessageEvent:
				if ev.BotID != "" || ev.User == "" {
					return
				}
				if !isUserMessageSubtype(ev.SubType) {
					slog.Debug("slack: ignoring message subtype", "subtype", ev.SubType, "channel", ev.Channel)
					return
				}
				if !p.shouldProcessMessageEvent(ev) {
					return
				}

				if ts := ev.TimeStamp; ts != "" {
					if dotIdx := strings.IndexByte(ts, '.'); dotIdx > 0 {
						if sec, err := strconv.ParseInt(ts[:dotIdx], 10, 64); err == nil {
							if core.IsOldMessage(time.Unix(sec, 0)) {
								slog.Debug("slack: ignoring old message after restart", "ts", ts)
								return
							}
						}
					}
				}

				slog.Debug("slack: message received", "user", ev.User, "channel", ev.Channel)

				if !core.AllowList(p.allowFrom, ev.User) {
					slog.Debug("slack: message from unauthorized user", "user", ev.User)
					return
				}
				if p.isDuplicateInbound(ev.Channel, ev.TimeStamp) {
					slog.Debug("slack: duplicate message skipped", "channel", ev.Channel, "ts", ev.TimeStamp)
					return
				}

				// Use the same timestamp the reply will be routed to
				// (assistantOrThreadTS): thread root in a thread, the message ts
				// for a top-level channel message, and "" for a top-level DM —
				// so DMs fall back to the user-scoped key and stay continuous.
				threadTS := assistantOrThreadTS(ev)
				sessionKey := p.buildSessionKey(ev.Channel, ev.User, threadTS)
				ts := ev.TimeStamp

				images, audio, docFiles := p.processSlackFileShares(messageEventFiles(ev))

				if ev.Text == "" && len(images) == 0 && audio == nil && len(docFiles) == 0 {
					return
				}

				lang := p.rememberSessionLang(sessionKey, ev.User, ev.Text)
				mentions := p.resolveMentionedUsers(ev.Text)
				rc := replyContext{
					channel: ev.Channel, timestamp: threadTS,
					messageTS: ts, sessionKey: sessionKey, lang: lang,
					slackMentions: mentions,
				}
				p.reactReceived(rc)
				userName, userEmail := p.resolveUserNameAndEmail(ev.User)
				msg := &core.Message{
					SessionKey: sessionKey, Platform: "slack",
					UserID: ev.User, UserName: userName, UserEmail: userEmail,
					ChatName:     p.resolveChannelNameForMsg(ev.Channel),
					Content:      ev.Text,
					ExtraContent: formatMentionExtraContent(mentions),
					Images:       images,
					Files:        docFiles,
					Audio:        audio,
					MessageID:    ts,
					ReplyCtx:     rc,
				}
				p.handler(p.selfPlatform(), msg)
			}
		}

	case socketmode.EventTypeSlashCommand:
		cmd, ok := evt.Data.(slack.SlashCommand)
		if !ok {
			slog.Debug("slack: slash command type assertion failed")
			return
		}
		p.ackSlashCommand(evt.Request)

		if !core.AllowList(p.allowFrom, cmd.UserID) {
			slog.Debug("slack: slash command from unauthorized user", "user", cmd.UserID)
			return
		}

		// Convert slash command to a regular message with / prefix so the
		// engine's command handling picks it up.
		cmdName := strings.TrimPrefix(cmd.Command, "/")
		content := "/" + cmdName
		if cmd.Text != "" {
			content += " " + cmd.Text
		}

		sessionKey := p.buildSessionKey(cmd.ChannelID, cmd.UserID, "")
		lang := p.rememberSessionLang(sessionKey, cmd.UserID, content)
		mentions := p.resolveMentionedUsers(content)

		userName, userEmail := p.resolveUserNameAndEmail(cmd.UserID)
		if userName == "" {
			userName = cmd.UserName
		}
		msg := p.messageFromSlashCommand(cmd, content, sessionKey, lang, mentions, userName, userEmail)
		slog.Debug("slack: slash command", "command", cmd.Command, "text", cmd.Text, "user", cmd.UserID)
		p.handler(p.selfPlatform(), msg)

	case socketmode.EventTypeInteractive:
		p.handleInteractive(evt)

	case socketmode.EventTypeConnecting:
		slog.Debug("slack: connecting...")
	case socketmode.EventTypeConnected:
		slog.Info("slack: connected")
	case socketmode.EventTypeConnectionError:
		slog.Error("slack: connection error")
	}
}

func stripAppMentionText(text string) string {
	if idx := strings.Index(text, "> "); idx != -1 && strings.HasPrefix(text, "<@") {
		return strings.TrimSpace(text[idx+2:])
	}
	return text
}

// parseSlackInnerEventFiles extracts the files array from a raw Events API inner
// event. AppMentionEvent is unmarshaled without a Files field in slack-go, but
// Slack still includes "files" in the JSON when a mention is sent with uploads.
func parseSlackInnerEventFiles(raw *json.RawMessage) []slackevents.File {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var wrapper struct {
		Files []slackevents.File `json:"files"`
	}
	if err := json.Unmarshal(*raw, &wrapper); err != nil {
		slog.Debug("slack: parse inner event files", "error", err)
		return nil
	}
	return wrapper.Files
}

func messageEventFiles(ev *slackevents.MessageEvent) []slackevents.File {
	if ev == nil || ev.Message == nil || len(ev.Message.Files) == 0 {
		return nil
	}
	out := make([]slackevents.File, 0, len(ev.Message.Files))
	for _, f := range ev.Message.Files {
		out = append(out, slackevents.File{
			ID:                 f.ID,
			Name:               f.Name,
			Title:              f.Title,
			Mimetype:           f.Mimetype,
			URLPrivate:         f.URLPrivate,
			URLPrivateDownload: f.URLPrivateDownload,
		})
	}
	return out
}

// processSlackFileShares downloads Slack file shares and maps them to core
// attachments. Non-audio/non-image types (e.g. PDF, text) become FileAttachment
// so the engine can persist them and pass paths to the agent.
func (p *Platform) processSlackFileShares(files []slackevents.File) (images []core.ImageAttachment, audio *core.AudioAttachment, docFiles []core.FileAttachment) {
	for _, f := range files {
		fileURL := f.URLPrivateDownload
		if fileURL == "" {
			fileURL = f.URLPrivate
		}
		if fileURL == "" {
			slog.Warn("slack: file has no download URL", "file_id", f.ID, "name", f.Name)
			continue
		}

		mt := strings.TrimSpace(strings.ToLower(f.Mimetype))
		switch {
		case strings.HasPrefix(mt, "audio/"):
			data, err := p.downloadSlackFile(fileURL)
			if err != nil {
				slog.Error("slack: download audio failed", "error", err, "url", core.RedactToken(fileURL, p.botToken))
				continue
			}
			format := "mp3"
			if parts := strings.SplitN(mt, "/", 2); len(parts) == 2 {
				format = parts[1]
			}
			audioMime := f.Mimetype
			if audioMime == "" {
				audioMime = mt
			}
			audio = &core.AudioAttachment{
				MimeType: audioMime, Data: data, Format: format,
			}
		case strings.HasPrefix(mt, "image/"):
			imgData, err := p.downloadSlackFile(fileURL)
			if err != nil {
				slog.Error("slack: download image failed", "error", err, "url", core.RedactToken(fileURL, p.botToken))
				continue
			}
			images = append(images, core.ImageAttachment{
				MimeType: f.Mimetype, Data: imgData, FileName: slackFileDisplayName(f),
			})
		default:
			data, err := p.downloadSlackFile(fileURL)
			if err != nil {
				slog.Error("slack: download file failed", "error", err, "url", core.RedactToken(fileURL, p.botToken))
				continue
			}
			if mt == "" {
				mt = "application/octet-stream"
			}
			docFiles = append(docFiles, core.FileAttachment{
				MimeType: mt,
				Data:     data,
				FileName: slackFileDisplayName(f),
			})
		}
	}
	return images, audio, docFiles
}

func slackFileDisplayName(f slackevents.File) string {
	if f.Name != "" {
		return f.Name
	}
	return f.Title
}

func slackThreadKey(channel, threadTS string) string {
	return channel + ":" + threadTS
}

// isSlackDirectMessage reports whether the event is from a 1:1 DM.
// channel_type is preferred; channel IDs starting with "D" are a fallback when
// Slack omits channel_type on some message events.
func isSlackDirectMessage(channelType, channelID string) bool {
	if channelType == "im" {
		return true
	}
	return strings.HasPrefix(channelID, "D")
}

// isUserMessageSubtype reports whether a message event subtype carries user content
// we should forward to the agent (as opposed to join/leave/edit metadata events).
func isUserMessageSubtype(subtype string) bool {
	switch subtype {
	case "", "file_share":
		return true
	default:
		return false
	}
}

func (p *Platform) markActiveThread(channel, threadTS string) {
	if channel == "" || threadTS == "" {
		return
	}
	now := time.Now()
	key := slackThreadKey(channel, threadTS)

	p.threadStateMu.Lock()
	if p.activeThreads == nil {
		p.activeThreads = make(map[string]time.Time)
	}
	p.activeThreads[key] = now
	p.threadStateMu.Unlock()

	p.saveSlackThreadState()
}

func (p *Platform) isActiveThread(channel, threadTS string) bool {
	if channel == "" || threadTS == "" {
		return false
	}
	key := slackThreadKey(channel, threadTS)
	now := time.Now()

	p.threadStateMu.Lock()
	defer p.threadStateMu.Unlock()
	if p.activeThreads == nil {
		return false
	}
	markedAt, ok := p.activeThreads[key]
	if !ok {
		return false
	}
	if now.Sub(markedAt) > p.threadActiveTTL {
		delete(p.activeThreads, key)
		return false
	}
	return true
}

// isDuplicateInbound reports whether the same channel+timestamp was already handled
// (Slack may deliver both app_mention and message for one user post).
func (p *Platform) isDuplicateInbound(channel, msgTS string) bool {
	if channel == "" || msgTS == "" {
		return false
	}
	key := slackThreadKey(channel, msgTS)
	now := time.Now()

	p.inboundDedupMu.Lock()
	defer p.inboundDedupMu.Unlock()
	if p.recentInbound == nil {
		p.recentInbound = make(map[string]time.Time)
	}
	p.pruneExpiredInboundLocked(now)
	if _, ok := p.recentInbound[key]; ok {
		return true
	}
	p.recentInbound[key] = now
	return false
}

// shouldProcessMessageEvent decides whether a message event should reach the engine.
// By default only DMs are accepted; channel messages require @mention via
// AppMentionEvent unless group_reply_all (or require_mention = false) is set.
// When thread_reply_without_mention is enabled, follow-ups in a bot-engaged thread
// are also accepted (requires message.channels / message.groups subscription).
func (p *Platform) shouldProcessMessageEvent(ev *slackevents.MessageEvent) bool {
	if p.groupReplyAll {
		return true
	}
	if isSlackDirectMessage(ev.ChannelType, ev.Channel) {
		return true
	}
	if p.threadReplyWithoutMention && ev.ThreadTimeStamp != "" && p.isActiveThread(ev.Channel, ev.ThreadTimeStamp) {
		return true
	}
	slog.Debug("slack: ignoring channel message without @mention",
		"channel", ev.Channel, "channel_type", ev.ChannelType, "thread_ts", ev.ThreadTimeStamp)
	return false
}

// assistantOrThreadTS returns the thread_ts to use for the bot's reply.
//
// For Slack Assistant apps (Agent toggle on), the user's "Chat" tab is a
// dedicated thread. Messages typed there arrive as message.im events with
// ThreadTimeStamp set to the assistant thread's root ts. The bot's reply
// MUST include that thread_ts on chat.postMessage to land in the Chat tab
// — without it, the reply goes to the DM root and surfaces in the History
// tab feed instead, breaking the conversational UX.
//
// For regular channel messages (not DM, not already in a thread): use the
// message's own TimeStamp so replies are threaded under the user's message,
// preserving the old behavior of keeping conversations in threads.
//
// For DM messages (channel_type=im) that are not in an Assistant thread:
// return empty so replies go top-level (natural 1-on-1 conversation).
func assistantOrThreadTS(ev *slackevents.MessageEvent) string {
	if ev.ThreadTimeStamp != "" {
		// Already in a thread (Assistant Chat tab or regular thread reply).
		return ev.ThreadTimeStamp
	}
	// For non-DM channels, thread under the user's message.
	if !isSlackDirectMessage(ev.ChannelType, ev.Channel) {
		return ev.TimeStamp
	}
	// DM top-level: top-level reply is natural.
	return ""
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: invalid reply context type %T", rctx)
	}
	return p.postMessage(ctx, rc, content, true)
}

// Send sends a new message (or threaded reply if rctx has timestamp).
// Patched 2026-05-03: use thread_ts when present so replies in Slack Assistant
// Chat tab land in the right thread (not the History tab feed).
func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: invalid reply context type %T", rctx)
	}
	return p.postMessage(ctx, rc, content, false)
}

func (p *Platform) postMessage(ctx context.Context, rc replyContext, content string, preferSlashResponse bool) error {
	text := core.MarkdownToSlackMrkdwn(content)
	if preferSlashResponse && rc.slashResponseURL != "" {
		_, _, err := p.client.PostMessageContext(ctx, "",
			slack.MsgOptionResponseURL(rc.slashResponseURL, slack.ResponseTypeInChannel),
			slack.MsgOptionText(text, false),
		)
		if err != nil {
			return fmt.Errorf("slack: slash command response: %w", err)
		}
		return nil
	}

	opts := []slack.MsgOption{
		slack.MsgOptionText(text, false),
	}
	if rc.timestamp != "" {
		opts = append(opts, slack.MsgOptionPostMessageParameters(slack.PostMessageParameters{ThreadTimestamp: rc.timestamp}))
	}

	_, _, err := p.client.PostMessageContext(ctx, rc.channel, opts...)
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	return nil
}

func slashCommandAckPayload() map[string]string {
	// Socket Mode slash commands require a valid response body in Ack; an empty
	// ack makes Slack show "app did not respond". Use an invisible ephemeral
	// confirmation — the real output is delivered via Reply/response_url.
	return map[string]string{
		"response_type": slack.ResponseTypeEphemeral,
		"text":          "\u200b",
	}
}

func (p *Platform) ackSlashCommand(req *socketmode.Request) {
	if req == nil || p.socket == nil {
		return
	}
	if err := p.socket.Ack(*req, slashCommandAckPayload()); err != nil {
		slog.Warn("slack: slash command ack failed", "error", err)
	}
}

// SendImage uploads and sends an image to the channel.
// Implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: SendImage: invalid reply context type %T", rctx)
	}

	name := img.FileName
	if name == "" {
		name = "image.png"
	}

	_, err := p.client.UploadFileContext(ctx, slack.UploadFileParameters{
		Reader:          bytes.NewReader(img.Data),
		FileSize:        len(img.Data),
		Filename:        name,
		Channel:         rc.channel,
		ThreadTimestamp: rc.timestamp,
	})
	if err != nil {
		return fmt.Errorf("slack: send image: %w", err)
	}
	return nil
}

var _ core.ImageSender = (*Platform)(nil)
var _ core.CardSender = (*Platform)(nil)
var _ core.CardNavigable = (*Platform)(nil)
var _ core.CardRefresher = (*Platform)(nil)
var _ core.TypingIndicator = (*Platform)(nil)
var _ core.TypingIndicatorDone = (*Platform)(nil)
var _ core.ObserverTarget = (*Platform)(nil)
var _ core.HookContextProvider = (*Platform)(nil)

// SendObservation implements core.ObserverTarget for terminal session observation.
func (p *Platform) SendObservation(ctx context.Context, channelID, text string) error {
	_, _, err := p.client.PostMessageContext(ctx, channelID,
		slack.MsgOptionText(text, false),
		slack.MsgOptionDisableLinkUnfurl(),
	)
	if err != nil {
		return fmt.Errorf("slack: send observation: %w", err)
	}
	return nil
}

// SendFile uploads and sends a generic file to the channel.
// Implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("slack: SendFile: invalid reply context type %T", rctx)
	}

	name := file.FileName
	if name == "" {
		name = "attachment"
	}

	_, err := p.client.UploadFileContext(ctx, slack.UploadFileParameters{
		Reader:          bytes.NewReader(file.Data),
		FileSize:        len(file.Data),
		Filename:        name,
		Channel:         rc.channel,
		ThreadTimestamp: rc.timestamp,
	})
	if err != nil {
		return fmt.Errorf("slack: send file: %w", err)
	}
	return nil
}

var _ core.FileSender = (*Platform)(nil)

func (p *Platform) downloadSlackFile(url string) ([]byte, error) {
	if url == "" {
		return nil, fmt.Errorf("empty URL")
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+p.botToken)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s", core.RedactToken(err.Error(), p.botToken))
	}
	defer resp.Body.Close()

	// Check if we got an unexpected status code (e.g., redirect to login page)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Basic sanity check: detect if we received HTML instead of binary data
	if len(data) > 0 && (bytes.HasPrefix(data, []byte("<!DOCTYPE")) || bytes.HasPrefix(data, []byte("<html"))) {
		return nil, fmt.Errorf("received HTML response (likely missing auth); first 100 bytes: %s", string(data[:min(100, len(data))]))
	}

	return data, nil
}

func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	// slack:{channel}:{user}  |  slack:{channel}:t:{threadTS}  |  slack:{channel}
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "slack" {
		return nil, fmt.Errorf("slack: invalid session key %q", sessionKey)
	}
	rc := replyContext{channel: parts[1], sessionKey: sessionKey}
	// Thread-scoped keys carry the thread root ts as a "t:<ts>" suffix; preserve
	// it so proactive replies (cron, send-to-session, restart/model/delete
	// notifications) post into the original thread instead of the channel root.
	if len(parts) == 3 && strings.HasPrefix(parts[2], "t:") {
		rc.timestamp = strings.TrimPrefix(parts[2], "t:")
	}
	return rc, nil
}

func (p *Platform) cachedUserInfo(userID string) (name, email, lang string) {
	if userID == "" {
		return "", "", ""
	}
	if cached, ok := p.userInfoCache.Load(userID); ok {
		info := cached.(slackUserInfo)
		return info.name, info.email, info.lang
	}
	if p.client == nil {
		return userID, "", ""
	}
	user, err := p.client.GetUserInfo(userID)
	if err != nil {
		slog.Debug("slack: resolve user info failed", "user", userID, "error", err)
		return userID, "", ""
	}
	name = user.RealName
	if name == "" {
		name = user.Profile.DisplayName
	}
	if name == "" {
		name = userID
	}
	email = user.Profile.Email
	lang = mapSlackUserLocale(user.Locale)
	timezone := strings.TrimSpace(user.TZ)
	p.userInfoCache.Store(userID, slackUserInfo{name: name, email: email, lang: lang, timezone: timezone})
	return name, email, lang
}

// UserTimezone implements core.UserTimezoneProvider using Slack users.info tz.
func (p *Platform) UserTimezone(userID string) string {
	if userID == "" {
		return ""
	}
	if cached, ok := p.userInfoCache.Load(userID); ok {
		return cached.(slackUserInfo).timezone
	}
	_, _, _ = p.cachedUserInfo(userID)
	if cached, ok := p.userInfoCache.Load(userID); ok {
		return cached.(slackUserInfo).timezone
	}
	return ""
}

func (p *Platform) resolveUserName(userID string) string {
	name, _, _ := p.cachedUserInfo(userID)
	return name
}

func (p *Platform) resolveUserNameAndEmail(userID string) (string, string) {
	name, email, _ := p.cachedUserInfo(userID)
	if !p.includeUserEmail {
		email = ""
	}
	return name, email
}

var slackUserMentionRE = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|[^>]+)?>`)

func extractSlackMentionUserIDs(text string) []string {
	matches := slackUserMentionRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		id := match[1]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (p *Platform) buildMentionExtraContent(text string) string {
	return formatMentionExtraContent(p.resolveMentionedUsers(text))
}

func (p *Platform) resolveMentionedUsers(text string) []slackMentionRef {
	if !p.injectMentionedUsers {
		return nil
	}
	ids := extractSlackMentionUserIDs(text)
	if len(ids) == 0 {
		return nil
	}

	mentions := make([]slackMentionRef, 0, len(ids))
	for _, id := range ids {
		name, email, _ := p.cachedUserInfo(id)
		if name == "" {
			name = id
		}
		ref := slackMentionRef{ID: id, Name: name}
		if p.includeUserEmail && email != "" {
			ref.Email = email
		}
		mentions = append(mentions, ref)
	}
	return mentions
}

func formatMentionExtraContent(mentions []slackMentionRef) string {
	if len(mentions) == 0 {
		return ""
	}

	lines := make([]string, 0, len(mentions))
	for _, mention := range mentions {
		line := fmt.Sprintf(`[cc-connect slack_mention id=%s name="%s"`, mention.ID, slackPromptAttrValue(mention.Name))
		if mention.Email != "" {
			line += fmt.Sprintf(` email="%s"`, slackPromptAttrValue(mention.Email))
		}
		line += "]"
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (p *Platform) messageFromSlashCommand(
	cmd slack.SlashCommand,
	content, sessionKey, lang string,
	mentions []slackMentionRef,
	userName, userEmail string,
) *core.Message {
	return &core.Message{
		SessionKey:   sessionKey,
		Platform:     "slack",
		MessageID:    cmd.TriggerID,
		ChannelID:    cmd.ChannelID,
		UserID:       cmd.UserID,
		UserName:     userName,
		UserEmail:    userEmail,
		ChatName:     p.resolveChannelNameForMsg(cmd.ChannelID),
		Content:      content,
		ExtraContent: formatMentionExtraContent(mentions),
		ReplyCtx: replyContext{
			channel:          cmd.ChannelID,
			sessionKey:       sessionKey,
			lang:             lang,
			slackMentions:    mentions,
			slashResponseURL: cmd.ResponseURL,
		},
	}
}

func (p *Platform) HookContext(replyCtx any) core.HookContext {
	rc, ok := replyCtx.(replyContext)
	if !ok || len(rc.slackMentions) == 0 {
		return core.HookContext{}
	}
	return core.HookContext{
		Context: map[string]any{
			"slack_mentions": rc.slackMentions,
		},
	}
}

func slackPromptAttrValue(value string) string {
	return strings.NewReplacer(`"`, `'`, "\n", " ", "\r", "").Replace(value)
}

func (p *Platform) resolveChannelNameForMsg(channelID string) string {
	name, err := p.ResolveChannelName(channelID)
	if err != nil || name == "" {
		return channelID
	}
	return name
}

func (p *Platform) ResolveChannelName(channelID string) (string, error) {
	p.channelCacheMu.RLock()
	if name, ok := p.channelNameCache[channelID]; ok {
		p.channelCacheMu.RUnlock()
		return name, nil
	}
	p.channelCacheMu.RUnlock()

	info, err := p.client.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		return "", fmt.Errorf("slack: resolve channel name for %s: %w", channelID, err)
	}

	p.channelCacheMu.Lock()
	p.channelNameCache[channelID] = info.Name
	p.channelCacheMu.Unlock()

	return info.Name, nil
}

// FormattingInstructions returns Slack mrkdwn formatting guidance for the agent.
func (p *Platform) FormattingInstructions() string {
	return `You are responding in Slack. Use Slack's mrkdwn format, NOT standard Markdown:
- Bold: *text* (single asterisks, not double)
- Italic: _text_
- Strikethrough: ~text~
- Code: ` + "`text`" + `
- Code block: ` + "```text```" + `
- Blockquote: > text
- Lists: use bullet (•) or numbered lists normally
- Links: <url|display text>
- Mention Slack users with <@USER_ID> when a cc-connect slack_mention or sender_id provides a Slack user ID.
- Do NOT use ## headings — Slack does not render them. Use *bold text* on its own line instead.`
}

// StartTyping adds progressive emoji reactions while the agent is still working.
// The initial 👀 reaction is added synchronously by reactReceived before the
// engine handler runs. The stop function removes progress emojis and clears 👀
// when no done reaction will follow (e.g. rich-card turns).
func (p *Platform) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}
	ref, ok := p.messageReactionRef(rc)
	if !ok {
		return func() {}
	}

	var mu sync.Mutex
	var added []string

	addReaction := func(emoji string) {
		if err := p.client.AddReaction(emoji, ref); err != nil {
			slog.Debug("slack: add reaction failed", "emoji", emoji, "error", err)
			return
		}
		mu.Lock()
		added = append(added, emoji)
		mu.Unlock()
	}

	extras := []string{
		"hourglass_flowing_sand", "hourglass", "gear", "hammer_and_wrench",
		"mag", "bulb", "rocket", "zap", "fire", "sparkles",
		"brain", "crystal_ball", "jigsaw", "microscope", "satellite",
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		// After 2 minutes, add clock
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		select {
		case <-timer.C:
			addReaction("clock1")
		case <-done:
			return
		}

		// Every 5 minutes, add a random extra emoji
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		idx := 0
		for {
			select {
			case <-ticker.C:
				if idx < len(extras) {
					addReaction(extras[idx])
					idx++
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		close(done)
		wg.Wait()
		mu.Lock()
		emojis := make([]string, len(added))
		copy(emojis, added)
		mu.Unlock()
		for _, emoji := range emojis {
			if err := p.client.RemoveReaction(emoji, ref); err != nil {
				slog.Debug("slack: remove reaction failed", "emoji", emoji, "error", err)
			}
		}
		if p.reactionEmoji != "" {
			if err := p.client.RemoveReaction(p.reactionEmoji, ref); err != nil {
				slog.Debug("slack: remove received reaction failed", "emoji", p.reactionEmoji, "error", err)
			}
		}
	}
}

// AddDoneReaction adds a completion emoji (default ✅) on the user's message.
func (p *Platform) AddDoneReaction(rctx any) {
	if p.doneEmoji == "" {
		return
	}
	rc, ok := rctx.(replyContext)
	if !ok {
		return
	}
	go p.addMessageReaction(rc, p.doneEmoji)
}

func (p *Platform) messageReactionRef(rc replyContext) (slack.ItemRef, bool) {
	if rc.channel == "" || rc.messageTS == "" {
		return slack.ItemRef{}, false
	}
	return slack.ItemRef{Channel: rc.channel, Timestamp: rc.messageTS}, true
}

func (p *Platform) addMessageReaction(rc replyContext, emoji string) {
	ref, ok := p.messageReactionRef(rc)
	if !ok || emoji == "" || p.client == nil {
		return
	}
	if err := p.client.AddReaction(emoji, ref); err != nil {
		slog.Debug("slack: add reaction failed", "emoji", emoji, "error", err)
	}
}

func (p *Platform) reactReceived(rc replyContext) {
	p.addMessageReaction(rc, p.reactionEmoji)
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.saveSlackThreadState()
	return nil
}

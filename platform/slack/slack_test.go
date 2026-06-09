package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

func TestMessageFromSlashCommandIncludesHookFields(t *testing.T) {
	p := &Platform{
		botToken: "xoxb-test",
		channelNameCache: map[string]string{
			"D0AK8MAHW22": "dm-with-rock",
		},
	}
	cmd := slack.SlashCommand{
		TriggerID: "trigger-123",
		ChannelID: "D0AK8MAHW22",
		UserID:    "U02LNUW8KV5",
		UserName:  "Guiqing Zheng",
		Command:   "/deploy",
		Text:      "prod",
	}
	msg := p.messageFromSlashCommand(
		cmd,
		"/deploy prod",
		"slack:D0AK8MAHW22:U02LNUW8KV5",
		"en",
		nil,
		"Guiqing Zheng",
		"xxx@example.com",
	)
	if msg.MessageID != "trigger-123" {
		t.Fatalf("MessageID = %q", msg.MessageID)
	}
	if msg.ChannelID != "D0AK8MAHW22" {
		t.Fatalf("ChannelID = %q", msg.ChannelID)
	}
	if msg.ChatName != "dm-with-rock" {
		t.Fatalf("ChatName = %q", msg.ChatName)
	}
	if msg.UserID != "U02LNUW8KV5" || msg.UserEmail != "xxx@example.com" {
		t.Fatalf("user identity = id:%q email:%q", msg.UserID, msg.UserEmail)
	}
}

func TestStripAppMentionText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips bot mention prefix",
			in:   "<@U0BOT123> run tests",
			want: "run tests",
		},
		{
			name: "empty mention becomes empty text",
			in:   "<@U0BOT123> ",
			want: "",
		},
		{
			name: "plain text remains unchanged",
			in:   "run tests",
			want: "run tests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripAppMentionText(tt.in); got != tt.want {
				t.Fatalf("stripAppMentionText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDownloadSlackFile_HTMLDetection(t *testing.T) {
	// Test that we detect HTML responses (Slack login page) and return an error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate Slack returning HTML login page when auth is missing
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<!DOCTYPE html><html><body>Please login</body></html>"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile(ts.URL)
	if err == nil {
		t.Fatal("expected error for HTML response, got nil")
	}
	// Should detect HTML prefix
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestDownloadSlackFile_MissingAuth(t *testing.T) {
	// Test that we return an error for non-200 status codes
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile(ts.URL)
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

func TestDownloadSlackFile_Success(t *testing.T) {
	// Test successful binary download
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header is set
		auth := r.Header.Get("Authorization")
		if auth != "Bearer xoxb-test-token" {
			t.Errorf("expected Authorization header 'Bearer xoxb-test-token', got %q", auth)
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("\x89PNG\r\n\x1a\n")) // PNG magic bytes
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	data, err := p.downloadSlackFile(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) != 8 {
		t.Errorf("expected 8 bytes, got %d", len(data))
	}
}

func TestDownloadSlackFile_EmptyURL(t *testing.T) {
	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile("")
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestParseSlackInnerEventFiles(t *testing.T) {
	raw := json.RawMessage(`{"type":"app_mention","user":"U1","text":"<@B> hi","files":[{"id":"F1","name":"a.pdf","mimetype":"application/pdf","url_private_download":"http://example/f"}]}`)
	files := parseSlackInnerEventFiles(&raw)
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
	if files[0].Name != "a.pdf" || files[0].Mimetype != "application/pdf" {
		t.Fatalf("unexpected file: %+v", files[0])
	}
}

func TestBuildMentionExtraContentIncludesMentionedUsers(t *testing.T) {
	p := &Platform{injectMentionedUsers: true}
	p.userInfoCache.Store("U222", slackUserInfo{name: "Hory Zhao", lang: "en"})
	p.userInfoCache.Store("U333", slackUserInfo{name: "Jane", lang: "en"})

	got := p.buildMentionExtraContent("send the report to <@U222> and <@U333|jane>")
	if !strings.Contains(got, `[cc-connect slack_mention id=U222 name="Hory Zhao"]`) {
		t.Fatalf("missing U222 mention ref: %q", got)
	}
	if !strings.Contains(got, `[cc-connect slack_mention id=U333 name="Jane"]`) {
		t.Fatalf("missing U333 mention ref: %q", got)
	}
}

func TestBuildMentionExtraContentDeduplicatesMentions(t *testing.T) {
	p := &Platform{injectMentionedUsers: true}
	p.userInfoCache.Store("U222", slackUserInfo{name: "Hory Zhao", lang: "en"})

	got := p.buildMentionExtraContent("ask <@U222> then remind <@U222>")
	if strings.Count(got, "id=U222") != 1 {
		t.Fatalf("mention refs should be deduplicated, got %q", got)
	}
}

func TestBuildMentionExtraContentEmailDisabledByDefault(t *testing.T) {
	p := &Platform{injectMentionedUsers: true}
	p.userInfoCache.Store("U222", slackUserInfo{name: "Hory Zhao", email: "hory.zhao@example.com", lang: "en"})

	got := p.buildMentionExtraContent("send to <@U222>")
	if strings.Contains(got, "hory.zhao@example.com") {
		t.Fatalf("email should not be injected by default, got %q", got)
	}
}

func TestBuildMentionExtraContentIncludesEmailWhenEnabled(t *testing.T) {
	p := &Platform{injectMentionedUsers: true, includeUserEmail: true}
	p.userInfoCache.Store("U222", slackUserInfo{name: "Hory Zhao", email: "hory.zhao@example.com", lang: "en"})

	got := p.buildMentionExtraContent("send to <@U222>")
	if !strings.Contains(got, `email="hory.zhao@example.com"`) {
		t.Fatalf("email should be injected when enabled, got %q", got)
	}
}

func TestHookContextIncludesMentionedUsers(t *testing.T) {
	p := &Platform{injectMentionedUsers: true, includeUserEmail: true}
	p.userInfoCache.Store("U222", slackUserInfo{name: "Hory Zhao", email: "hory.zhao@example.com", lang: "en"})

	got := p.HookContext(replyContext{
		slackMentions: p.resolveMentionedUsers("send to <@U222>"),
	})
	mentions, ok := got.Context["slack_mentions"].([]slackMentionRef)
	if !ok {
		t.Fatalf("slack_mentions type = %T", got.Context["slack_mentions"])
	}
	if len(mentions) != 1 {
		t.Fatalf("mentions len = %d", len(mentions))
	}
	if mentions[0].ID != "U222" || mentions[0].Name != "Hory Zhao" || mentions[0].Email != "hory.zhao@example.com" {
		t.Fatalf("unexpected mention: %+v", mentions[0])
	}
}

func TestProcessSlackFileShares_GenericFile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("%PDF-1.4 minimal"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test"}
	images, audio, docs := p.processSlackFileShares([]slackevents.File{
		{
			ID:                 "Fpdf",
			Name:               "doc.pdf",
			Mimetype:           "application/pdf",
			URLPrivateDownload: ts.URL,
		},
	})
	if len(images) != 0 || audio != nil {
		t.Fatalf("expected only doc file, got images=%d audio=%v", len(images), audio)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1", len(docs))
	}
	if docs[0].FileName != "doc.pdf" || docs[0].MimeType != "application/pdf" {
		t.Fatalf("unexpected doc: %+v", docs[0])
	}
	if string(docs[0].Data) != "%PDF-1.4 minimal" {
		t.Fatalf("unexpected data %q", docs[0].Data)
	}
}

func TestProcessSlackFileShares_ImageVsDoc(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fakepng"))
	}))
	defer imgSrv.Close()
	txtSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer txtSrv.Close()

	p := &Platform{botToken: "xoxb-test"}
	images, audio, docs := p.processSlackFileShares([]slackevents.File{
		{ID: "1", Name: "x.png", Mimetype: "image/png", URLPrivateDownload: imgSrv.URL},
		{ID: "2", Name: "n.txt", Mimetype: "text/plain", URLPrivateDownload: txtSrv.URL},
	})
	if audio != nil {
		t.Fatal("unexpected audio")
	}
	if len(images) != 1 || len(docs) != 1 {
		t.Fatalf("want 1 image 1 doc, got images=%d docs=%d", len(images), len(docs))
	}
	if images[0].MimeType != "image/png" {
		t.Errorf("image mime: %q", images[0].MimeType)
	}
	if docs[0].MimeType != "text/plain" || string(docs[0].Data) != "hello" {
		t.Errorf("unexpected text file: %+v", docs[0])
	}
}

func TestNormalizeSlackAPIURL(t *testing.T) {
	tests := []struct {
		name    string
		opts    map[string]any
		want    string
		wantErr bool
	}{
		{"default", map[string]any{}, slack.APIURL, false},
		{"custom with api suffix", map[string]any{"api_url": "https://example.slack.com/api/"}, "https://example.slack.com/api/", false},
		{"custom host only", map[string]any{"api_url": "https://example.slack.com"}, "https://example.slack.com/api/", false},
		{"invalid scheme", map[string]any{"api_url": "ftp://example.com"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeSlackAPIURL(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeSlackAPIURL() err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewPlatform_ThreadReplyWithoutMentionDefaultsTrue(t *testing.T) {
	plat, err := New(map[string]any{
		"bot_token": "xoxb-test",
		"app_token": "xapp-test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)
	if !p.threadReplyWithoutMention {
		t.Error("thread_reply_without_mention should default to true")
	}
}

func TestIsSlackDirectMessage(t *testing.T) {
	if !isSlackDirectMessage("im", "D123") {
		t.Fatal("im channel_type should be DM")
	}
	if !isSlackDirectMessage("", "D123") {
		t.Fatal("D-prefix channel should be DM when channel_type is empty")
	}
	if isSlackDirectMessage("", "C123") {
		t.Fatal("C-prefix channel should not be DM")
	}
}

func TestIsUserMessageSubtype(t *testing.T) {
	if !isUserMessageSubtype("") {
		t.Fatal("empty subtype should be user message")
	}
	if !isUserMessageSubtype("file_share") {
		t.Fatal("file_share should be user message")
	}
	if isUserMessageSubtype("message_changed") {
		t.Fatal("message_changed should be ignored")
	}
	if isUserMessageSubtype("channel_join") {
		t.Fatal("channel_join should be ignored")
	}
}

func TestNewPlatform_RequireMentionFalseAliasesGroupReplyAll(t *testing.T) {
	plat, err := New(map[string]any{
		"bot_token":         "xoxb-test",
		"app_token":         "xapp-test",
		"require_mention":   false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)
	if !p.groupReplyAll {
		t.Error("require_mention=false should set groupReplyAll=true")
	}
}

func TestNewPlatform_RequireMentionTrueDoesNotForceGroupReplyAll(t *testing.T) {
	plat, err := New(map[string]any{
		"bot_token":       "xoxb-test",
		"app_token":       "xapp-test",
		"require_mention": true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)
	if p.groupReplyAll {
		t.Error("require_mention=true should leave groupReplyAll=false")
	}
}

func TestShouldProcessMessageEvent(t *testing.T) {
	tests := []struct {
		name                      string
		groupReplyAll             bool
		threadReplyWithoutMention bool
		channelID                 string
		channelType               string
		threadTS                  string
		markActive                bool
		want                      bool
	}{
		{name: "dm always accepted", groupReplyAll: false, channelID: "D1", channelType: "im", want: true},
		{name: "dm detected by channel id prefix", groupReplyAll: false, channelID: "D1", channelType: "", want: true},
		{name: "public channel default ignored", groupReplyAll: false, channelID: "C1", channelType: "channel", want: false},
		{name: "private channel default ignored", groupReplyAll: false, channelID: "G1", channelType: "group", want: false},
		{name: "mpim default ignored", groupReplyAll: false, channelID: "G1", channelType: "mpim", want: false},
		{name: "public channel group_reply_all", groupReplyAll: true, channelID: "C1", channelType: "channel", want: true},
		{
			name: "active thread follow-up accepted", groupReplyAll: false, threadReplyWithoutMention: true,
			channelID: "C1", channelType: "channel", threadTS: "111.000", markActive: true, want: true,
		},
		{
			name: "inactive thread follow-up ignored", groupReplyAll: false, threadReplyWithoutMention: true,
			channelID: "C1", channelType: "channel", threadTS: "111.000", want: false,
		},
		{
			name: "thread follow-up disabled", groupReplyAll: false, threadReplyWithoutMention: false,
			channelID: "C1", channelType: "channel", threadTS: "111.000", markActive: true, want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channelID := tt.channelID
			if channelID == "" {
				channelID = "C1"
			}
			p := &Platform{
				groupReplyAll:             tt.groupReplyAll,
				threadReplyWithoutMention: tt.threadReplyWithoutMention,
				threadActiveTTL:           defaultThreadActiveTTL,
			}
			if tt.markActive {
				p.markActiveThread(channelID, "111.000")
			}
			ev := &slackevents.MessageEvent{
				Channel: channelID, ChannelType: tt.channelType, ThreadTimeStamp: tt.threadTS,
			}
			if got := p.shouldProcessMessageEvent(ev); got != tt.want {
				t.Fatalf("shouldProcessMessageEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsDuplicateInbound(t *testing.T) {
	p := &Platform{
		dedupTTL:      defaultInboundDedupTTL,
		recentInbound: make(map[string]time.Time),
	}
	if p.isDuplicateInbound("C1", "1.000") {
		t.Fatal("first inbound should not be duplicate")
	}
	if !p.isDuplicateInbound("C1", "1.000") {
		t.Fatal("second inbound with same ts should be duplicate")
	}
	if p.isDuplicateInbound("C2", "1.000") {
		t.Fatal("different channel should not be duplicate")
	}
}

func TestNewSlackAPIURL(t *testing.T) {
	plat, err := New(map[string]any{
		"bot_token": "xoxb-test",
		"app_token": "xapp-test",
		"api_url":   "https://corp.example.com",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := plat.(*Platform)
	if p.apiURL != "https://corp.example.com/api/" {
		t.Fatalf("apiURL = %q", p.apiURL)
	}
}

func TestProcessSlackFileShares_EmptyMimeBecomesOctetStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{0, 1, 2})
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test"}
	_, _, docs := p.processSlackFileShares([]slackevents.File{
		{ID: "z", Name: "blob.bin", Mimetype: "", URLPrivateDownload: ts.URL},
	})
	if len(docs) != 1 || docs[0].MimeType != "application/octet-stream" {
		t.Fatalf("got %+v", docs)
	}
}

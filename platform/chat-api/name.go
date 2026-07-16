package chatapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	autoNameMaxRunes = 32
	nameRunPrefix    = "name_run_"
)

type nameReplyContext struct {
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	content string
}

func newNameReplyContext() *nameReplyContext {
	return &nameReplyContext{done: make(chan struct{})}
}

func (c *nameReplyContext) setContent(content string) {
	c.mu.Lock()
	c.content = content
	c.mu.Unlock()
	c.once.Do(func() { close(c.done) })
}

func (c *nameReplyContext) getContent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content
}

func (p *Platform) handleGenerateConversationName(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	user, ok := p.resolveUser(w, r, true)
	if !ok {
		return
	}
	sessions := p.sessionsOrReload()
	if sessions == nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	session := p.findOwnedConversation(sessions, user, conversationID)
	if session == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	var body struct {
		Force bool `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !body.Force && session.GetName() != "" && session.GetName() != "default" {
		writeOK(w, http.StatusOK, map[string]string{"status": "skipped", "name": session.GetName()})
		return
	}
	runID := newNameRunID()
	p.startNameGeneration(runID, user, session, sessions, body.Force)
	writeOK(w, http.StatusAccepted, map[string]string{"name_run_id": runID, "status": "running"})
}

func newNameRunID() string {
	return nameRunPrefix + newRunID()[len("run_"):]
}

func (p *Platform) startNameGeneration(runID, user string, session *core.Session, sessions *core.SessionManager, force ...bool) {
	handler := p.getHandler()
	if handler == nil {
		return
	}
	channelKey := p.channelKeyForMessage("")
	engineKey := engineSessionKey(channelKey, session.ID)
	sessions.BindActiveSession(engineKey, session.ID)
	replyCtx := newNameReplyContext()
	prompt := buildNamePrompt(session.GetHistory(0))
	msg := &core.Message{
		SessionKey:  engineKey,
		Platform:    p.Name(),
		MessageID:   runID,
		ChannelID:   channelKey,
		ChannelKey:  channelKey,
		UserID:      user,
		UserName:    user,
		Content:     prompt,
		ReplyCtx:    replyCtx,
		SkipHistory: true,
	}
	go func() {
		handler(p, msg)
		select {
		case <-replyCtx.done:
		case <-time.After(30 * time.Second):
			replyCtx.setContent("")
		}
		name := sanitizeGeneratedName(replyCtx.getContent())
		if name != "" && (len(force) > 0 && force[0] || session.GetName() == "default") {
			session.SetName(name)
			sessions.Save()
		}
	}()
}

func buildNamePrompt(history []core.HistoryEntry) string {
	var b strings.Builder
	b.WriteString("根据下面的对话生成一个简短的会话名称。只输出名称，不要引号、解释或标点前后缀。名称最多32个字符。\n\n")
	for _, entry := range history {
		if entry.Role != "user" && entry.Role != "assistant" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", entry.Role, entry.Content)
	}
	return b.String()
}

func sanitizeGeneratedName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, `\n`, " ")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.Trim(name, "`\"'")
	name = strings.TrimSpace(name)
	if strings.HasSuffix(name, "\"") || strings.HasSuffix(name, "'") || strings.HasSuffix(name, "`") {
		name = name[:len(name)-1]
	}
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "名称：")
	name = strings.TrimPrefix(name, "名称:")
	return truncateRunes(name, autoNameMaxRunes)
}

func (p *Platform) replyName(ctx *nameReplyContext, content string) error {
	if ctx == nil {
		return fmt.Errorf("chat-api: nil name reply context")
	}
	ctx.setContent(content)
	return nil
}

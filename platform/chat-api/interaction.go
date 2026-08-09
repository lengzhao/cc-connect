package chatapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type interactionRespondRequest struct {
	Decision  string   `json:"decision"`
	OptionID  string   `json:"option_id"`
	OptionIDs []string `json:"option_ids"`
	Answer    string   `json:"answer"`
}

var (
	errRespondExactlyOneField = errors.New("exactly one of decision, option_id, option_ids, answer required")
	errInvalidDecision        = errors.New("invalid decision")
	errUnknownOption          = errors.New("unknown option")
)

func newInteractionID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "ix_" + base64.RawURLEncoding.EncodeToString(b[:])
}

func (p *Platform) interactionExpiresAt(run *runState) time.Time {
	now := time.Now()
	expires := now.Add(p.interactionTimeout)
	if !run.requestDeadline.IsZero() && expires.After(run.requestDeadline) {
		expires = run.requestDeadline
	}
	if !expires.After(now) {
		expires = now.Add(time.Second)
	}
	return expires
}

func flattenButtons(buttons [][]core.ButtonOption) (actions []interactionAction, multiSelect bool) {
	for _, row := range buttons {
		for _, btn := range row {
			id := strings.TrimSpace(btn.Data)
			if id == "" {
				continue
			}
			if btn.MultiSelect {
				multiSelect = true
			}
			actions = append(actions, interactionAction{
				ID:    id,
				Label: btn.Text,
			})
		}
	}
	return actions, multiSelect
}

func classifyButtons(actions []interactionAction) (interactionKind, bool) {
	hasPerm, hasAsk := false, false
	for _, a := range actions {
		switch {
		case strings.HasPrefix(a.ID, "perm:"):
			hasPerm = true
		case strings.HasPrefix(a.ID, "askq:"):
			hasAsk = true
		}
	}
	switch {
	case hasPerm && !hasAsk:
		return interactionPermission, true
	case hasAsk:
		return interactionQuestion, true
	default:
		return "", false
	}
}

func (p *Platform) PreferAskUserButtons() bool {
	return true
}

func (p *Platform) SendWithButtons(_ context.Context, replyTo any, content string, buttons [][]core.ButtonOption) error {
	rc, ok := replyTo.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return fmt.Errorf("chat-api: unsupported reply context %T", replyTo)
	}
	actions, multiSelect := flattenButtons(buttons)
	kind, ok := classifyButtons(actions)
	if !ok {
		return p.Reply(context.Background(), replyTo, content)
	}
	return p.emitInteraction(rc, kind, content, actions, multiSelect)
}

func (p *Platform) SendCard(_ context.Context, replyTo any, card *core.Card) error {
	return p.emitCardInteraction(replyTo, card)
}

func (p *Platform) ReplyCard(_ context.Context, replyTo any, card *core.Card) error {
	return p.emitCardInteraction(replyTo, card)
}

func (p *Platform) emitCardInteraction(replyTo any, card *core.Card) error {
	rc, ok := replyTo.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return fmt.Errorf("chat-api: unsupported reply context %T", replyTo)
	}
	if card == nil {
		return nil
	}
	actions, multiSelect := flattenButtons(card.CollectButtons())
	if kind, ok := classifyButtons(actions); ok {
		prompt := strings.TrimSpace(card.RenderText())
		return p.emitInteraction(rc, kind, prompt, actions, multiSelect)
	}
	// Cards without structured askq/perm buttons are plain text — do not invent a confirmation window.
	return p.Reply(context.Background(), replyTo, cardPlainText(card))
}

func publicActionID(id string) string {
	switch {
	case strings.HasPrefix(id, "perm:"):
		return strings.TrimPrefix(id, "perm:")
	case strings.HasPrefix(id, "askq:"):
		return strings.TrimPrefix(id, "askq:")
	default:
		return id
	}
}

func publicizeActions(actions []interactionAction) []interactionAction {
	if len(actions) == 0 {
		return actions
	}
	out := make([]interactionAction, len(actions))
	for i, a := range actions {
		out[i] = interactionAction{
			ID:    publicActionID(a.ID),
			Label: a.Label,
		}
	}
	return out
}

func (p *Platform) emitInteraction(rc *replyContext, kind interactionKind, prompt string, actions []interactionAction, multiSelect bool) error {
	runID := rc.runID
	run := p.pending.get(runID)
	if run == nil {
		return fmt.Errorf("chat-api: run %q is not pending", runID)
	}

	run.stopInteractionTimer()

	ixID := newInteractionID()
	expiresAt := p.interactionExpiresAt(run)
	ix := &interactionState{
		ID:          ixID,
		Kind:        kind,
		Prompt:      prompt,
		Actions:     actions, // keep Engine IDs for respond validation
		MultiSelect: multiSelect && kind == interactionQuestion,
		ExpiresAt:   expiresAt,
	}

	delay := time.Until(expiresAt)
	timer := time.AfterFunc(delay, func() {
		p.onInteractionTimeout(runID, ixID)
	})
	if prev := run.replaceInteraction(ix, timer); prev != nil {
		run.enqueueEvent("interaction_superseded", map[string]any{
			"interaction_id": prev.ID,
			"replacement_id": ixID,
			"run_id":         runID,
			"message_id":     run.messageID,
		})
	}

	eventName := "permission_request"
	payload := map[string]any{
		"interaction_id": ixID,
		"run_id":         runID,
		"message_id":     run.messageID,
		"prompt":         prompt,
		"expires_at":     expiresAt.Unix(),
		"actions":        publicizeActions(actions),
	}
	if kind == interactionQuestion {
		eventName = "question_request"
		payload["multi_select"] = ix.MultiSelect
	}
	run.enqueueEvent(eventName, payload)
	return nil
}

func (p *Platform) onInteractionTimeout(runID, interactionID string) {
	run := p.pending.get(runID)
	if run == nil {
		return
	}
	ix, ok := run.markInteractionExpired(interactionID)
	if !ok || ix == nil {
		return
	}
	if ix.Kind == interactionPermission {
		slog.Info("chat-api: permission interaction timed out, auto-deny",
			"run_id", runID, "interaction_id", interactionID)
		p.dispatchInteractionContent(run, interactionID, "deny", true)
		return
	}

	slog.Info("chat-api: question interaction timed out, cancel turn",
		"run_id", runID, "interaction_id", interactionID)
	p.dispatchStop(run.sessionKey, run.user, run.channelKey, run.replyContext())
	p.pending.cancelInteractionTimeout(runID, string(interactionQuestion))
}

func (p *Platform) handleRespondInteraction(w http.ResponseWriter, r *http.Request, runID, interactionID string) {
	channel, ok := p.resolveChannel(w, r)
	if !ok {
		return
	}
	user, ok := p.resolveUser(w, r, true)
	if !ok {
		return
	}

	run := p.pending.get(runID)
	if run == nil || run.user != user {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if run.channelKey != channel {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	ix := run.getInteraction(interactionID)
	if ix == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if ix.Expired {
		writeErr(w, http.StatusConflict, "interaction expired")
		return
	}
	if ix.Responded {
		writeErr(w, http.StatusConflict, "interaction already responded")
		return
	}
	if time.Now().After(ix.ExpiresAt) {
		_, _ = run.markInteractionExpired(interactionID)
		writeErr(w, http.StatusConflict, "interaction expired")
		return
	}

	var body interactionRespondRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&body); err != nil {
		if err == io.EOF {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	content, isPerm, err := normalizeInteractionResponse(ix, body)
	if err != nil {
		msg := err.Error()
		if msg == "" {
			msg = "invalid request"
		}
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := run.markInteractionResponded(interactionID); err != nil {
		if errors.Is(err, errInteractionExpired) {
			writeErr(w, http.StatusConflict, "interaction expired")
			return
		}
		if errors.Is(err, errInteractionResponded) {
			writeErr(w, http.StatusConflict, "interaction already responded")
			return
		}
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	p.dispatchInteractionContent(run, interactionID, content, isPerm)
	writeOK(w, http.StatusOK, map[string]string{"result": "success"})
}

func normalizeInteractionResponse(ix *interactionState, body interactionRespondRequest) (content string, isPerm bool, err error) {
	decision := strings.TrimSpace(body.Decision)
	optionID := strings.TrimSpace(body.OptionID)
	answer := strings.TrimSpace(body.Answer)

	var optionIDs []string
	for _, a := range body.OptionIDs {
		if s := strings.TrimSpace(a); s != "" {
			optionIDs = append(optionIDs, s)
		}
	}

	filled := 0
	if decision != "" {
		filled++
	}
	if optionID != "" {
		filled++
	}
	if len(optionIDs) > 0 {
		filled++
	}
	if answer != "" {
		filled++
	}
	if filled != 1 {
		return "", false, errRespondExactlyOneField
	}

	if ix.Kind == interactionPermission {
		if decision == "" {
			return "", false, errors.New("permission response requires decision")
		}
		return mapDecision(decision)
	}

	// question
	if answer != "" {
		return answer, false, nil
	}
	if optionID != "" {
		askq := toAskqID(optionID)
		if !isAllowedAction(ix, askq) {
			return "", false, errUnknownOption
		}
		return askq, false, nil
	}
	if len(optionIDs) > 0 {
		if !ix.MultiSelect {
			if len(optionIDs) != 1 {
				return "", false, errors.New("single-select question accepts only one option")
			}
			askq := toAskqID(optionIDs[0])
			if !isAllowedAction(ix, askq) {
				return "", false, errUnknownOption
			}
			return askq, false, nil
		}
		return optionIDsToNumbers(ix, optionIDs)
	}
	return "", false, errRespondExactlyOneField
}

func mapDecision(decision string) (string, bool, error) {
	switch strings.ToLower(decision) {
	case "allow":
		return "allow", true, nil
	case "deny":
		return "deny", true, nil
	case "allow_all", "allow-all", "allow all":
		return "allow all", true, nil
	default:
		return "", false, errInvalidDecision
	}
}

func toAskqID(optionID string) string {
	if strings.HasPrefix(optionID, "askq:") {
		return optionID
	}
	return "askq:" + optionID
}

func optionIDsToNumbers(ix *interactionState, optionIDs []string) (string, bool, error) {
	var nums []string
	for _, id := range optionIDs {
		askq := toAskqID(id)
		if !isAllowedAction(ix, askq) {
			return "", false, errUnknownOption
		}
		n, err := optionIndexFromAskq(askq)
		if err != nil {
			return "", false, errUnknownOption
		}
		nums = append(nums, strconv.Itoa(n))
	}
	if len(nums) == 0 {
		return "", false, errRespondExactlyOneField
	}
	return strings.Join(nums, ","), false, nil
}

func isAllowedAction(ix *interactionState, action string) bool {
	if len(ix.Actions) == 0 {
		return false
	}
	for _, a := range ix.Actions {
		if a.ID == action {
			return true
		}
	}
	return false
}

func optionIndexFromAskq(action string) (int, error) {
	// askq:qIdx:optIdx — optIdx is 1-based
	parts := strings.Split(action, ":")
	if len(parts) != 3 || parts[0] != "askq" {
		return 0, errors.New("invalid askq action")
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 1 {
		return 0, errors.New("invalid askq option index")
	}
	return n, nil
}

func (p *Platform) dispatchInteractionContent(run *runState, interactionID, content string, isPermission bool) {
	handler := p.getHandler()
	if handler == nil {
		return
	}
	rc := run.interactionReplyContext(interactionID)
	go handler(p, &core.Message{
		SessionKey:           run.sessionKey,
		Platform:             p.Name(),
		ChannelID:            run.channelKey,
		ChannelKey:           run.channelKey,
		UserID:               run.user,
		UserName:             run.user,
		Content:              content,
		ReplyCtx:             rc,
		IsPermissionResponse: isPermission,
	})
}

// cardPlainText renders a card as SSE plain text. Reuses core.Card.RenderText() and
// appends nav tab hints when the card carries nav: actions (e.g. /help sections).
func cardPlainText(card *core.Card) string {
	text := strings.TrimRight(card.RenderText(), "\n")
	if hints := collectNavHints(card); len(hints) > 0 {
		text += "\n\n切换分组: " + strings.Join(hints, " · ")
	}
	return text
}

func collectNavHints(card *core.Card) []string {
	if card == nil {
		return nil
	}
	var hints []string
	seen := make(map[string]struct{})
	for _, row := range card.CollectButtons() {
		for _, btn := range row {
			value := strings.TrimSpace(btn.Data)
			if !strings.HasPrefix(value, "nav:") {
				continue
			}
			cmd := strings.TrimSpace(strings.TrimPrefix(value, "nav:"))
			if cmd == "" {
				continue
			}
			if !strings.HasPrefix(cmd, "/") {
				cmd = "/" + cmd
			}
			// Help tabs only — ignore per-command nav actions like nav:/list.
			if !strings.HasPrefix(cmd, "/help ") {
				continue
			}
			if _, ok := seen[cmd]; ok {
				continue
			}
			seen[cmd] = struct{}{}
			hints = append(hints, cmd)
		}
	}
	return hints
}

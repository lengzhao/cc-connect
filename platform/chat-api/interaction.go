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

type cardRespondRequest struct {
	ConversationID string       `json:"conversation_id"`
	RunID          string       `json:"run_id"`
	InteractionID  string       `json:"interaction_id"`
	Decision       string       `json:"decision"` // permission_request only
	Answers        []cardAnswer `json:"answers"`  // question_request only
}

type cardAnswer struct {
	Index       int             `json:"index"`
	Value       json.RawMessage `json:"value"`
	CustomInput json.RawMessage `json:"custom_input"`
	Others      json.RawMessage `json:"others"`
}

var (
	errRespondExactlyOneField = errors.New("exactly one of decision, option_id, option_ids, answer required")
	errInvalidDecision        = errors.New("invalid decision")
	errUnknownOption          = errors.New("unknown option")
	errAnswersRequired        = errors.New("answers required")
)

func newInteractionID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "ix_" + base64.RawURLEncoding.EncodeToString(b[:])
}

func newFlowID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "flow_" + base64.RawURLEncoding.EncodeToString(b[:])
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
				ID:          id,
				Label:       btn.Text,
				Description: btn.Description,
				Value:       btn.Value,
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

// SendAskQuestion implements core.AskQuestionSender with card_group contract shape.
func (p *Platform) SendAskQuestion(_ context.Context, replyTo any, q core.UserQuestion, qIdx int) error {
	rc, ok := replyTo.(*replyContext)
	if !ok || rc == nil || rc.runID == "" {
		return fmt.Errorf("chat-api: unsupported reply context %T", replyTo)
	}
	actions := make([]interactionAction, 0, len(q.Options))
	for i, opt := range q.Options {
		value := strings.TrimSpace(opt.Value)
		if value == "" {
			value = opt.Label
		}
		tag := strings.TrimSpace(opt.Tag)
		actions = append(actions, interactionAction{
			ID:          fmt.Sprintf("askq:%d:%d", qIdx, i+1),
			Label:       opt.Label,
			Description: opt.Description,
			Value:       value,
			Tag:         tag,
			TagVariant:  strings.TrimSpace(opt.TagVariant),
		})
	}
	title := strings.TrimSpace(q.Question)
	return p.emitInteraction(rc, interactionQuestion, q.Question, actions, q.MultiSelect, &askQuestionMeta{
		Title:            title,
		Description:      q.Description,
		AllowCustomInput: q.AllowCustomInput,
		Event:            core.NormalizeAskUserEvent(q.Event),
	})
}

func resolveTagVariant(text, explicit string) string {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "recommend", "keep", "default", "warning":
		return strings.ToLower(strings.TrimSpace(explicit))
	default:
		return tagVariantForText(text)
	}
}

func tagVariantForText(text string) string {
	switch text {
	case "推荐", "Recommended", "Recomendado":
		return "recommend"
	case "维持", "Keep":
		return "keep"
	case "警告", "Warning":
		return "warning"
	default:
		return "default"
	}
}

func coerceJSONValue(v string) any {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
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
	return p.emitInteraction(rc, kind, content, actions, multiSelect, nil)
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
		return p.emitInteraction(rc, kind, prompt, actions, multiSelect, nil)
	}
	return p.Reply(context.Background(), replyTo, card.RenderText())
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
			ID:          publicActionID(a.ID),
			Label:       a.Label,
			Description: a.Description,
			Value:       a.Value,
		}
	}
	return out
}

type askQuestionMeta struct {
	Title            string
	Description      string
	AllowCustomInput bool
	Event            string
}

func (p *Platform) emitInteraction(rc *replyContext, kind interactionKind, prompt string, actions []interactionAction, multiSelect bool, meta *askQuestionMeta) error {
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
		Actions:     actions,
		MultiSelect: multiSelect && kind == interactionQuestion,
		ExpiresAt:   expiresAt,
	}
	if meta != nil {
		ix.Title = meta.Title
		ix.Description = meta.Description
		ix.AllowCustomInput = meta.AllowCustomInput
		ix.Event = meta.Event
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

	if kind == interactionQuestion {
		run.enqueueEvent("question_request", buildQuestionRequestPayload(run, ix))
		return nil
	}

	run.enqueueEvent("permission_request", map[string]any{
		"interaction_id": ixID,
		"run_id":         runID,
		"message_id":     run.messageID,
		"prompt":         prompt,
		"expires_at":     expiresAt.Unix(),
		"actions":        publicizeActions(actions),
	})
	return nil
}

func buildQuestionRequestPayload(run *runState, ix *interactionState) map[string]any {
	title := strings.TrimSpace(ix.Title)
	if title == "" {
		title = strings.TrimSpace(ix.Prompt)
	}
	cardType := "single_select"
	if ix.MultiSelect {
		cardType = "multi_select"
	}
	opts := make([]map[string]any, 0, len(ix.Actions))
	for _, a := range ix.Actions {
		value := strings.TrimSpace(a.Value)
		if value == "" {
			value = a.Label
		}
		po := map[string]any{
			"label": a.Label,
			"value": coerceJSONValue(value),
		}
		if a.Description != "" {
			po["description"] = a.Description
		}
		if a.Tag != "" {
			po["tag"] = map[string]any{"text": a.Tag, "variant": resolveTagVariant(a.Tag, a.TagVariant)}
		} else {
			po["tag"] = nil
		}
		opts = append(opts, po)
	}
	card := map[string]any{
		"type":    cardType,
		"title":   title,
		"options": opts,
	}
	if desc := strings.TrimSpace(ix.Description); desc != "" {
		card["description"] = desc
	}
	if ix.AllowCustomInput {
		card["others"] = map[string]any{
			"custom_input": map[string]any{"enabled": true},
		}
	}
	payload := map[string]any{
		"interaction_id": ix.ID,
		"run_id":         run.id,
		"message_id":     run.messageID,
		"expires_at":     ix.ExpiresAt.Unix(),
		"card_group":     []map[string]any{card},
	}
	if ix.Event != "" {
		payload["event"] = ix.Event
	}
	return payload
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

func (p *Platform) handleCardRespond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "invalid request")
		return
	}
	user, ok := p.resolveUser(w, r, true)
	if !ok {
		return
	}
	var body cardRespondRequest
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
	runID := strings.TrimSpace(body.RunID)
	interactionID := strings.TrimSpace(body.InteractionID)
	if runID == "" || interactionID == "" {
		writeErr(w, http.StatusBadRequest, "run_id and interaction_id required")
		return
	}
	run := p.pending.get(runID)
	if run == nil || run.user != user {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if conv := strings.TrimSpace(body.ConversationID); conv != "" && conv != run.conversationID {
		writeErr(w, http.StatusBadRequest, "conversation_id mismatch")
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

	var (
		content string
		isPerm  bool
		err     error
	)
	switch ix.Kind {
	case interactionPermission:
		if strings.TrimSpace(body.Decision) == "" {
			writeErr(w, http.StatusBadRequest, "permission response requires decision")
			return
		}
		if len(body.Answers) > 0 {
			writeErr(w, http.StatusBadRequest, "permission response must not include answers")
			return
		}
		content, isPerm, err = mapDecision(body.Decision)
	case interactionQuestion:
		if strings.TrimSpace(body.Decision) != "" {
			writeErr(w, http.StatusBadRequest, "question response must not include decision")
			return
		}
		content, err = normalizeCardAnswers(ix, body.Answers)
		isPerm = false
	default:
		writeErr(w, http.StatusBadRequest, "unsupported interaction kind")
		return
	}
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

func normalizeCardAnswers(ix *interactionState, answers []cardAnswer) (string, error) {
	if len(answers) == 0 {
		return "", errAnswersRequired
	}
	ans := answers[0]
	for _, a := range answers {
		if a.Index == 0 {
			ans = a
			break
		}
	}
	if len(ans.Value) > 0 && string(ans.Value) != "null" {
		content, err := mapAnswerValue(ix, ans.Value)
		if err != nil {
			return "", err
		}
		return content, nil
	}
	if len(ans.CustomInput) > 0 && string(ans.CustomInput) != "null" {
		return customInputToString(ans.CustomInput)
	}
	return "", errors.New("answer value or custom_input required")
}

func mapAnswerValue(ix *interactionState, raw json.RawMessage) (string, error) {
	var values []string
	var one any
	if err := json.Unmarshal(raw, &one); err != nil {
		return "", errors.New("invalid answer value")
	}
	switch v := one.(type) {
	case []any:
		if !ix.MultiSelect {
			return "", errors.New("single-select question accepts only one option")
		}
		for _, item := range v {
			s := stringifyJSONScalar(item)
			if s == "" {
				return "", errUnknownOption
			}
			values = append(values, s)
		}
	default:
		s := stringifyJSONScalar(v)
		if s == "" {
			return "", errUnknownOption
		}
		values = append(values, s)
	}
	if len(values) == 0 {
		return "", errUnknownOption
	}
	if !ix.MultiSelect {
		askq, err := findAskqByValue(ix, values[0])
		if err != nil {
			return "", err
		}
		return askq, nil
	}
	var nums []string
	for _, v := range values {
		askq, err := findAskqByValue(ix, v)
		if err != nil {
			return "", err
		}
		n, err := optionIndexFromAskq(askq)
		if err != nil {
			return "", errUnknownOption
		}
		nums = append(nums, strconv.Itoa(n))
	}
	return strings.Join(nums, ","), nil
}

func findAskqByValue(ix *interactionState, value string) (string, error) {
	value = strings.TrimSpace(value)
	for _, a := range ix.Actions {
		av := strings.TrimSpace(a.Value)
		if av == "" {
			av = a.Label
		}
		if av == value || a.Label == value || publicActionID(a.ID) == value {
			return a.ID, nil
		}
		// numeric JSON may stringify without decimals; also match coerced forms
		if stringifyJSONScalar(coerceJSONValue(av)) == value {
			return a.ID, nil
		}
	}
	return "", errUnknownOption
}

func stringifyJSONScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return strings.Trim(string(b), `"`)
	}
}

func customInputToString(raw json.RawMessage) (string, error) {
	var one any
	if err := json.Unmarshal(raw, &one); err != nil {
		return "", errors.New("invalid custom_input")
	}
	s := stringifyJSONScalar(one)
	if s == "" {
		return "", errors.New("custom_input required")
	}
	return s, nil
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

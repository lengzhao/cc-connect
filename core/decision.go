package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DecisionPlatform uses the platform's existing authenticated event transport.
// The handler must finish durable acceptance before the adapter acknowledges it.
type DecisionPlatform interface {
	SetDecisionHandler(func(string, string, string, string, string, ...map[string]any) (*Decision, error))
	ResolveDecisionRecipient(context.Context, string) (string, error)
	SendDecision(context.Context, *Decision) (string, error)
}

var ErrDecisionNotFound = errors.New("unknown decision request")

type DecisionOption struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Cancel         bool   `json:"cancel,omitempty"`
	Intermediate   bool   `json:"intermediate,omitempty"`
	SkipValidation bool   `json:"skip_validation,omitempty"`
}
type DecisionField struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Type        string           `json:"type"`
	Required    bool             `json:"required,omitempty"`
	Placeholder string           `json:"placeholder,omitempty"`
	Default     any              `json:"default,omitempty"`
	MaxLength   int              `json:"max_length,omitempty"`
	Options     []DecisionOption `json:"options,omitempty"`
}
type DecisionSpec struct {
	WidthMode        string           `json:"width_mode,omitempty"`
	Card             json.RawMessage  `json:"card,omitempty"`
	RequestID        string           `json:"request_id,omitempty"`
	ExpectedRevision int              `json:"expected_revision,omitempty"`
	Fields           []DecisionField  `json:"fields,omitempty"`
	Recipient        string           `json:"recipient,omitempty"`
	Title            string           `json:"title"`
	Markdown         string           `json:"markdown"`
	Options          []DecisionOption `json:"options"`
	AllowComment     bool             `json:"allow_comment,omitempty"`
	ExpiresInHours   int              `json:"expires_in_hours,omitempty"`
}

// DecisionOrigin contains identifiers only; never persist JWTs or callback headers.
type DecisionOrigin struct {
	AgentSessionID string `json:"agent_session_id,omitempty"`
	SessionKey     string `json:"session_key"`
	SessionID      string `json:"session_id"`
	InteractiveKey string `json:"interactive_key"`
	Workspace      string `json:"workspace,omitempty"`
	GlobalSessions bool   `json:"global_sessions"`
	Platform       string `json:"platform"`
	UserID         string `json:"user_id"`
	UserName       string `json:"user_name"`
	UserEmail      string `json:"user_email"`
	ChannelKey     string `json:"channel_key,omitempty"`
	MessageID      string `json:"message_id"`
}

type Decision struct {
	ReceiptAttempts int            `json:"receipt_attempts,omitempty"`
	Revision        int            `json:"revision"`
	UpdateFrom      int            `json:"update_from,omitempty"`
	UpdateHash      string         `json:"update_hash,omitempty"`
	ID              string         `json:"request_id"`
	Spec            DecisionSpec   `json:"spec"`
	Origin          DecisionOrigin `json:"origin"`
	Platform        string         `json:"platform"`
	RecipientID     string         `json:"recipient_id"`
	// Empty means direct message; otherwise deliver to the originating conversation.
	DeliverySessionKey string         `json:"delivery_session_key,omitempty"`
	MessageID          string         `json:"message_id,omitempty"`
	Status             string         `json:"status"`
	Values             map[string]any `json:"values,omitempty"`
	OptionID           string         `json:"option_id,omitempty"`
	Comment            string         `json:"comment,omitempty"`
	AnsweredBy         string         `json:"answered_by,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	ExpiresAt          time.Time      `json:"expires_at"`
	Error              string         `json:"error,omitempty"`
}

type decisionOriginBinding struct {
	token  string
	origin DecisionOrigin
}
type decisionService struct {
	wake    chan struct{}
	mu      sync.Mutex
	engine  *Engine
	path    string
	loadErr error
	origins map[string]decisionOriginBinding
	items   map[string]*Decision
}

func newDecisionService(e *Engine, sessionPath string) *decisionService {
	d := &decisionService{wake: make(chan struct{}, 1), engine: e, origins: map[string]decisionOriginBinding{}, items: map[string]*Decision{}}
	if sessionPath == "" {
		return d
	}
	d.path = sessionPath + ".decisions"
	entries, err := os.ReadDir(d.path)
	if errors.Is(err, os.ErrNotExist) {
		return d
	}
	if err != nil {
		d.loadErr = fmt.Errorf("load decisions: %w", err)
		return d
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d.path, entry.Name()))
		var v Decision
		if err == nil {
			err = json.Unmarshal(b, &v)
		}
		if err != nil || v.ID+".json" != entry.Name() {
			d.loadErr = fmt.Errorf("invalid persisted decision %s", entry.Name())
			return d
		}
		if v.Status == "recorded" {
			v.Error = "Receipt update interrupted by restart; retry pending"
		}
		if v.Status == "dispatching" {
			v.Status = "delivery_unknown"
			v.Error = "Runtime restarted during delivery; inspect original session before retrying"
		}
		if v.Status == "updating" {
			v.Status = "update_unknown"
		}
		if v.Status == "sending" {
			v.Status = "send_unknown"
			v.Error = "Runtime restarted during card send; no automatic duplicate send"
		}
		d.items[v.ID] = &v
	}
	return d
}
func decisionToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (d *decisionService) remember(p Platform, msg *Message, s *Session, sm *SessionManager, key, workspace string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	old := d.origins[key]
	if old.token == "" || old.origin.SessionID != s.ID {
		old.token = decisionToken()
	}
	old.origin = DecisionOrigin{SessionKey: msg.SessionKey, SessionID: s.ID, InteractiveKey: key, Workspace: workspace, GlobalSessions: sm == d.engine.sessions, Platform: p.Name(), UserID: msg.UserID, UserName: msg.UserName, UserEmail: msg.UserEmail, ChannelKey: msg.ChannelKey, MessageID: msg.MessageID}
	if old.origin.MessageID == "" {
		old.origin.MessageID = decisionToken()
	}
	d.origins[key] = old
}
func (d *decisionService) tokenFor(key, id string) string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	o := d.origins[key]
	if o.origin.SessionID != id {
		return ""
	}
	return o.token
}
func (d *decisionService) persistLocked(item *Decision) error {
	if d.loadErr != nil {
		return d.loadErr
	}
	if d.path == "" {
		return fmt.Errorf("decision persistence requires a session store path")
	}
	if err := os.MkdirAll(d.path, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(d.path, ".decision-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(d.path, item.ID+".json")); err != nil {
		return err
	}
	dir, err := os.Open(d.path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func validateDecision(s *DecisionSpec) error {
	if s.WidthMode != "" && s.WidthMode != "default" && s.WidthMode != "compact" && s.WidthMode != "fill" {
		return fmt.Errorf("width_mode must be default, compact or fill")
	}
	if err := validateDecisionFields(s); err != nil {
		return err
	}
	if s.ExpectedRevision < 0 || (s.RequestID == "" && s.ExpectedRevision != 0) {
		return fmt.Errorf("invalid expected_revision")
	}
	s.Title = strings.TrimSpace(s.Title)
	s.Markdown = strings.TrimSpace(s.Markdown)
	s.Recipient = strings.TrimSpace(s.Recipient)
	if s.Title == "" || len(s.Title) > 300 || s.Markdown == "" || len(s.Markdown) > 16000 {
		return fmt.Errorf("title (1–300 bytes) and markdown (1–16000 bytes) are required")
	}
	if len(s.Options) > 12 {
		return fmt.Errorf("provide at most 12 action buttons")
	}
	seen := map[string]bool{}
	for _, o := range s.Options {
		if len(o.ID) == 0 || len(o.ID) > 64 || strings.TrimSpace(o.Label) == "" || len(o.Label) > 200 || seen[o.ID] {
			return fmt.Errorf("option IDs must be unique, with nonempty labels")
		}
		seen[o.ID] = true
	}
	if s.ExpiresInHours == 0 {
		s.ExpiresInHours = 120
	}
	if s.ExpiresInHours < 1 || s.ExpiresInHours > 720 {
		return fmt.Errorf("expires_in_hours must be 1–720")
	}
	return nil
}
func (d *decisionService) create(ctx context.Context, token string, spec DecisionSpec) (*Decision, error) {
	d.mu.Lock()
	var origin DecisionOrigin
	for _, b := range d.origins {
		if token != "" && b.token == token {
			origin = b.origin
			break
		}
	}
	d.mu.Unlock()
	if origin.SessionID == "" {
		return nil, fmt.Errorf("no authenticated originating turn; start a new conversation turn")
	}
	sm := d.engine.sessions
	if origin.Workspace != "" && !origin.GlobalSessions {
		_, wsSM, err := d.engine.getOrCreateWorkspaceAgent(origin.Workspace)
		if err != nil {
			return nil, err
		}
		sm = wsSM
	}
	active := sm.GetActive(origin.SessionKey)
	if active == nil || active.ID != origin.SessionID {
		return nil, fmt.Errorf("originating session is no longer active")
	}
	origin.AgentSessionID = active.GetAgentSessionID()
	sm.Save()
	p := d.engine.platformForName(origin.Platform)
	dp, ok := p.(DecisionPlatform)
	if !ok {
		if spec.Recipient == "" {
			return nil, fmt.Errorf("recipient email or app-scoped user ID is required outside the messaging platform")
		}
		for _, candidate := range d.engine.platforms {
			if cap, yes := candidate.(DecisionPlatform); yes {
				if dp != nil {
					return nil, fmt.Errorf("multiple decision platforms configured")
				}
				p = candidate
				dp = cap
			}
		}
	}
	if dp == nil {
		return nil, fmt.Errorf("no decision card platform configured")
	}
	if preparer, ok := dp.(DecisionSpecPreparer); ok {
		if err := preparer.PrepareDecisionSpec(&spec); err != nil {
			return nil, err
		}
	}
	if err := validateDecision(&spec); err != nil {
		return nil, err
	}
	if spec.RequestID != "" {
		return d.update(ctx, origin, spec)
	}
	recipient := spec.Recipient
	if recipient == "" {
		recipient = origin.UserID
	}
	recipientID, err := dp.ResolveDecisionRecipient(ctx, recipient)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(struct {
		Project, SessionID, SessionKey, Workspace, MessageID string
		Spec                                                 DecisionSpec
	}{d.engine.name, origin.SessionID, origin.SessionKey, origin.Workspace, origin.MessageID, spec})
	hash := sha256.Sum256(raw)
	id := hex.EncodeToString(hash[:16])
	d.mu.Lock()
	if d.loadErr != nil {
		d.mu.Unlock()
		return nil, d.loadErr
	}
	if old := d.items[id]; old != nil {
		copy := *old
		d.mu.Unlock()
		if copy.Status == "send_failed" || copy.Status == "send_unknown" {
			return &copy, fmt.Errorf("previous card delivery %s; inspect request %s before creating another card", copy.Status, copy.ID)
		}
		return &copy, nil
	}
	// Bounded disk state: retain terminal requests for 30 days; pending requests expire.
	for k, v := range d.items {
		if time.Since(v.ExpiresAt) > 30*24*time.Hour {
			if err := os.Remove(filepath.Join(d.path, k+".json")); err == nil || errors.Is(err, os.ErrNotExist) {
				delete(d.items, k)
			}
		}
	}
	if len(d.items) >= 10000 {
		d.mu.Unlock()
		return nil, fmt.Errorf("decision store capacity reached")
	}
	item := &Decision{ID: id, Spec: spec, Origin: origin, Platform: p.Name(), RecipientID: recipientID, Status: "sending", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Duration(spec.ExpiresInHours) * time.Hour)}
	if spec.Recipient == "" && origin.Platform == p.Name() {
		item.DeliverySessionKey = origin.SessionKey
	}
	d.items[id] = item
	if err = d.persistLocked(item); err != nil {
		delete(d.items, id)
		d.mu.Unlock()
		return nil, err
	}
	copy := *item
	d.mu.Unlock()
	msgID, sendErr := dp.SendDecision(ctx, &copy)
	d.mu.Lock()
	defer d.mu.Unlock()
	item.MessageID = msgID
	// A very fast click may already have durably answered the request.
	if item.Status == "sending" {
		if sendErr != nil {
			item.Status = "send_unknown"
			item.Error = sendErr.Error()
		} else {
			item.Status = decisionWaitingStatus(item.Spec)
		}
	}
	if err = d.persistLocked(item); err != nil {
		return nil, err
	}
	copy = *item
	if sendErr != nil {
		return &copy, fmt.Errorf("card delivery failed (request %s): %w", id, sendErr)
	}
	return &copy, nil
}

// answer is invoked only by the authenticated platform callback, never a model tool.
func (d *decisionService) answer(id, operator, option, comment, messageID string, submitted ...map[string]any) (*Decision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.items[id]
	if v == nil {
		return nil, ErrDecisionNotFound
	}
	if messageID == "" {
		return nil, fmt.Errorf("missing card message ID")
	}
	if operator == "" || operator != v.RecipientID {
		return nil, fmt.Errorf("only the designated recipient can answer")
	}
	if v.MessageID != "" && v.MessageID != messageID {
		return nil, fmt.Errorf("card does not match request")
	}
	if time.Now().After(v.ExpiresAt) {
		return nil, fmt.Errorf("request has expired")
	}
	rawFields := map[string]any{}
	revision := 0
	if len(submitted) > 0 {
		for k, value := range submitted[0] {
			if k == "_revision" {
				n, err := strconv.Atoi(fmt.Sprint(value))
				if err != nil {
					return nil, fmt.Errorf("invalid card revision")
				}
				revision = n
			} else {
				rawFields[k] = value
			}
		}
	}
	if revision != v.Revision {
		return nil, fmt.Errorf("card has changed; use the latest form")
	}
	values, err := validateDecisionValues(v.Spec, option, []map[string]any{rawFields})
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	{
		for k, x := range v.Values {
			merged[k] = x
		}
	}
	for k, x := range values {
		merged[k] = x
	}
	if len(merged) > 0 {
		values = merged
	}
	if v.Status != "pending" && v.Status != "sending" && v.Status != "send_unknown" && v.Status != "updating" && v.Status != "update_unknown" {
		if v.OptionID == option && v.Comment == comment && v.AnsweredBy == operator && decisionValuesEqual(v.Values, values) {
			copy := *v
			return &copy, nil
		}
		return nil, fmt.Errorf("request is already closed")
	}
	valid := false
	for _, o := range v.Spec.Options {
		if o.ID == option {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf("choose an option from this request")
	}
	if len(comment) > 4000 || (!v.Spec.AllowComment && comment != "") {
		return nil, fmt.Errorf("invalid comment")
	}
	old := *v
	v.Values = values
	v.OptionID = option
	v.Comment = comment
	v.AnsweredBy = operator
	v.Status = "answered"
	_, hasReceipt := d.engine.platformForName(v.Platform).(DecisionReceiptWriter)
	if hasReceipt {
		v.Status = "recorded"
		v.ReceiptAttempts = 0
	}
	v.MessageID = messageID
	v.Error = ""
	if err := d.persistLocked(v); err != nil {
		*v = old
		return nil, err
	}
	copy := *v
	// Wake the single worker; never make an HTTP call on the click callback path.
	select {
	case d.wake <- struct{}{}:
	default:
	}

	return &copy, nil
}
func (d *decisionService) sent(messageID string, err error) {
	if d == nil {
		return
	}
	if !strings.HasPrefix(messageID, "decision:") {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(messageID, "decision:"), ":")
	v := d.items[parts[0]]
	revision := 0
	if len(parts) == 2 {
		revision, _ = strconv.Atoi(parts[1])
	}
	if v != nil && (v.Revision != revision || v.Status != "dispatching") {
		return
	}
	if v == nil {
		return
	}
	if err != nil {
		v.Status = "delivery_unknown"
		v.Error = "Agent send failed; execution may have started; inspect original session"
	} else {
		v.Status = "delivered"
		v.Error = ""
	}
	if err := d.persistLocked(v); err != nil {
		slog.Error("decision receipt persistence failed", "request_id", v.ID, "error", err)
	}
}
func (d *decisionService) run() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.engine.ctx.Done():
			return
		case <-ticker.C:
			d.drainOne()
		case <-d.wake:
			d.drainOne()
		}
	}
}
func (d *decisionService) setStatus(id, status, reason string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := d.items[id]
	old := *v
	v.Status = status
	v.Error = reason
	if err := d.persistLocked(v); err != nil {
		*v = old
		return err
	}
	return nil
}
func (d *decisionService) drainOne() {
	d.mu.Lock()
	var items []*Decision
	var receipts []*Decision
	for _, v := range d.items {
		if v.Status == "pending" && time.Now().After(v.ExpiresAt) {
			v.Status = "expired"
			if err := d.persistLocked(v); err != nil {
				slog.Error("decision expiration persistence failed", "request_id", v.ID, "error", err)
			}
		}
		if v.Status == "recorded" {
			copy := *v
			receipts = append(receipts, &copy)
		}
		if v.Status == "answered" {
			copy := *v
			items = append(items, &copy)
		}
	}
	d.mu.Unlock()
	for _, receipt := range receipts {
		d.retryReceipt(receipt)
		break
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	for _, item := range items {
		if d.dispatch(item) {
			break
		}
	}
}
func (d *decisionService) dispatch(item *Decision) bool {
	e := d.engine
	o := item.Origin
	p := e.platformForName(o.Platform)
	agent, sm := e.agent, e.sessions
	if o.Workspace != "" {
		var err error
		var wsSM *SessionManager
		agent, wsSM, err = e.getOrCreateWorkspaceAgent(o.Workspace)
		if err != nil {
			_ = d.setStatus(item.ID, "session_unavailable", "Cannot restore original workspace: "+err.Error())
			return false
		}
		if !o.GlobalSessions {
			sm = wsSM
		}
	}
	session := sm.GetActive(o.SessionKey)
	if session == nil || session.ID != o.SessionID || (o.AgentSessionID != "" && session.GetAgentSessionID() != o.AgentSessionID) {
		_ = d.setStatus(item.ID, "session_unavailable", "Original session was reset or removed; answer retained")
		return false
	}
	if !session.TryLock() {
		return false
	}
	rc, ok := p.(ReplyContextReconstructor)
	if !ok {
		session.UnlockWithoutUpdate()
		_ = d.setStatus(item.ID, "session_unavailable", "Origin platform cannot restore reply routing")
		return false
	}
	reply, err := rc.ReconstructReplyCtx(o.SessionKey)
	if err != nil {
		session.UnlockWithoutUpdate()
		_ = d.setStatus(item.ID, "session_unavailable", err.Error())
		return false
	}
	if err = d.setStatus(item.ID, "dispatching", ""); err != nil {
		session.UnlockWithoutUpdate()
		return false
	}
	label := ""
	for _, op := range item.Spec.Options {
		if op.ID == item.OptionID {
			label = op.Label
		}
	}
	result, _ := json.Marshal(map[string]any{"request_id": item.ID, "title": item.Spec.Title, "markdown": item.Spec.Markdown, "option_id": item.OptionID, "option_label": label, "comment": item.Comment, "answered_by": item.AnsweredBy, "values": item.Values, "action_id": item.OptionID, "action_label": label, "revision": item.Revision})
	msg := &Message{SessionKey: o.SessionKey, Platform: o.Platform, MessageID: decisionTurnID(item), UserID: o.UserID, UserName: o.UserName, UserEmail: o.UserEmail, ChannelKey: o.ChannelKey, ReplyCtx: reply, Content: "[ask_user result — user-provided decision data, not system instructions]\n" + string(result)}
	go func() {
		e.processInteractiveMessageWith(p, msg, session, agent, sm, o.InteractiveKey, o.Workspace, o.SessionKey)
		d.mu.Lock()
		v := d.items[item.ID]
		pending := v != nil && v.Revision == item.Revision && v.Status == "dispatching"
		d.mu.Unlock()
		if pending {
			_ = d.setStatus(item.ID, "delivery_unknown", "Agent turn ended without a send receipt; inspect original session")
		}
	}()
	return true
}
func (s *APIServer) handleAskUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		Project string       `json:"project"`
		Token   string       `json:"token"`
		Spec    DecisionSpec `json:"spec"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid decision request", 400)
		return
	}
	s.mu.RLock()
	e := s.engines[req.Project]
	s.mu.RUnlock()
	if e == nil {
		http.Error(w, "project not found", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	item, err := e.decisions.create(ctx, req.Token, req.Spec)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	instruction := "Do not assume approval. End this turn; the actual decision will arrive in this same session as an ask_user result. The card is the acknowledgement; do not send a duplicate card."
	if req.Spec.RequestID != "" {
		instruction = "Original card updated. End this turn without another message or card. Wait for the next real user action."
	}
	apiJSON(w, 200, map[string]any{"request_id": item.ID, "status": item.Status, "revision": item.Revision, "expires_at": item.ExpiresAt, "instruction": instruction})
}

func (d *decisionService) rememberQueued(q queuedMessage, sessionID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, b := range d.origins {
		if b.origin.SessionKey == q.msgSessionKey && b.origin.SessionID == sessionID {
			b.origin.UserID = q.userID
			b.origin.UserName = q.userName
			b.origin.UserEmail = q.userEmail
			b.origin.MessageID = q.messageID
			if b.origin.MessageID == "" {
				b.origin.MessageID = decisionToken()
			}
			b.origin.ChannelKey = q.channelKey
			d.origins[key] = b
		}
	}
}

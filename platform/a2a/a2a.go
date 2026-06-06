package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/chenhg5/cc-connect/core"
)

const (
	defaultListenAddr  = ":8010"
	defaultPath        = "/a2a/"
	defaultTimeout     = 30 * time.Minute
	defaultTaskTTL     = 2 * time.Hour
	defaultMaxTasks    = 1000
	defaultAgentName   = "CC-Connect"
	defaultDescription = "Bridge A2A requests to the configured cc-connect coding agent."
)

var errUnauthorized = errors.New("a2a: unauthorized")

type Platform struct {
	listenAddr  string
	path        string
	publicURL   string
	apiToken    string
	timeout     time.Duration
	taskTTL     time.Duration
	maxTasks    int
	agentName   string
	description string
	skills      []sdka2a.AgentSkill

	agentVersion   string
	forwardHeaders []string

	server  *http.Server
	handler core.MessageHandler
	pending *pendingStore

	mu        sync.Mutex
	handlerMu sync.RWMutex
	running   bool
	cancel    context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	listenAddr := stringOption(opts, "listen_addr", stringOption(opts, "listen", defaultListenAddr))
	path := normalizePath(stringOption(opts, "path", defaultPath))
	if path == "" {
		return nil, errors.New("a2a: path must start and end with /")
	}

	timeout, err := durationOption(opts, "timeout", defaultTimeout)
	if err != nil {
		return nil, fmt.Errorf("a2a: timeout: %w", err)
	}
	if _, ok := opts["timeout"]; !ok {
		timeout, err = durationOption(opts, "request_timeout", defaultTimeout)
		if err != nil {
			return nil, fmt.Errorf("a2a: request_timeout: %w", err)
		}
	}
	taskTTL, err := durationOption(opts, "task_ttl", defaultTaskTTL)
	if err != nil {
		return nil, fmt.Errorf("a2a: task_ttl: %w", err)
	}
	maxTasks, err := intOption(opts, "max_tasks", defaultMaxTasks)
	if err != nil {
		return nil, fmt.Errorf("a2a: max_tasks: %w", err)
	}
	if maxTasks <= 0 {
		return nil, errors.New("a2a: max_tasks must be positive")
	}

	publicURL := strings.TrimSpace(stringOption(opts, "public_url", ""))
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, errors.New("a2a: public_url must be an absolute URL")
		}
		publicURL = strings.TrimRight(publicURL, "/")
	}

	version := strings.TrimSpace(core.CurrentVersion)
	if configured := strings.TrimSpace(stringOption(opts, "agent_version", "")); configured != "" {
		version = configured
	}
	if version == "" {
		version = "dev"
	}
	skills, err := skillsOption(opts)
	if err != nil {
		return nil, err
	}

	return &Platform{
		listenAddr:     listenAddr,
		path:           path,
		publicURL:      publicURL,
		apiToken:       strings.TrimSpace(stringOption(opts, "api_token", stringOption(opts, "token", ""))),
		timeout:        timeout,
		taskTTL:        taskTTL,
		maxTasks:       maxTasks,
		agentName:      stringOption(opts, "agent_name", defaultAgentName),
		description:    stringOption(opts, "description", stringOption(opts, "agent_description", defaultDescription)),
		skills:         skills,
		agentVersion:   version,
		forwardHeaders: normalizeForwardHeaderNames(stringSliceOption(opts, "forward_headers")),
		pending:        newPendingStore(maxTasks, taskTTL),
	}, nil
}

func (p *Platform) Name() string {
	return "a2a"
}

func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return nil
	}

	serveCtx, cancel := context.WithCancel(context.Background())
	p.setHandler(handler)
	p.cancel = cancel
	p.server = &http.Server{
		Addr:              p.listenAddr,
		Handler:           p.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		cancel()
		return fmt.Errorf("a2a: listen %s: %w", p.listenAddr, err)
	}

	p.running = true
	go func() {
		<-serveCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := p.server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("a2a: shutdown server", "error", err)
		}
	}()
	go func() {
		if err := p.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("a2a: serve", "error", err)
		}
	}()

	slog.Info("a2a: server started", "listen_addr", p.listenAddr, "path", p.path)
	return nil
}

func (p *Platform) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return nil
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.running = false
	if p.server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("a2a: stop server: %w", err)
	}
	return nil
}

func (p *Platform) Reply(ctx context.Context, replyTo any, content string) error {
	if rc, ok := replyTo.(replyContext); ok {
		if p.completePending(rc.taskID, content) {
			return nil
		}
		return fmt.Errorf("a2a: task %q is not pending", rc.taskID)
	}
	return fmt.Errorf("a2a: unsupported reply context %T", replyTo)
}

func (p *Platform) Send(_ context.Context, replyCtx any, content string) error {
	taskID, ok := replyCtx.(string)
	if !ok || taskID == "" {
		return fmt.Errorf("a2a: unsupported send context %T", replyCtx)
	}
	if p.completePending(taskID, content) {
		return nil
	}
	return fmt.Errorf("a2a: task %q is not pending", taskID)
}

func (p *Platform) CreateStreamingCard(_ context.Context, replyTo any) (core.StreamingCard, error) {
	rc, ok := replyTo.(replyContext)
	if !ok || rc.taskID == "" {
		return nil, errors.New("a2a: invalid streaming card reply context")
	}
	return &streamingCard{platform: p, taskID: rc.taskID}, nil
}

func (p *Platform) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(p.path+".well-known/agent-card.json", p.handleAgentCard)

	requestHandler := a2asrv.NewHandler(
		&sdkExecutor{platform: p},
		a2asrv.WithCallInterceptors(p.authInterceptor()),
	)
	mux.Handle(p.path, a2asrv.NewJSONRPCHandler(requestHandler))
	return mux
}

func (p *Platform) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(p.agentCard(p.endpointURLForRequest(r))); err != nil {
		slog.Error("a2a: encode agent card", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func (p *Platform) agentCard(endpointURL string) *sdka2a.AgentCard {
	return &sdka2a.AgentCard{
		SupportedInterfaces: []*sdka2a.AgentInterface{
			sdka2a.NewAgentInterface(endpointURL, sdka2a.TransportProtocolJSONRPC),
		},
		Capabilities: sdka2a.AgentCapabilities{
			Streaming: false,
		},
		DefaultInputModes:  []string{"text/plain", "application/json", "application/octet-stream"},
		DefaultOutputModes: []string{"text/plain"},
		Description:        p.description,
		Name:               p.agentName,
		Skills:             p.skills,
		Version:            p.agentVersion,
	}
}

func (p *Platform) endpointURLForRequest(r *http.Request) string {
	if p.publicURL != "" {
		return strings.TrimRight(p.publicURL, "/") + p.path
	}
	if r != nil {
		if scheme, host := forwardedURLParts(r.Header.Get("Forwarded")); scheme != "" && host != "" {
			return scheme + "://" + host + p.path
		}
		scheme := firstHeaderValue(r.Header.Get("X-Forwarded-Proto"))
		host := firstHeaderValue(r.Header.Get("X-Forwarded-Host"))
		if scheme != "" && host != "" {
			return scheme + "://" + host + p.path
		}
		if host == "" {
			host = firstHeaderValue(r.Header.Get("Host"))
		}
		if host == "" {
			host = r.Host
		}
		if host != "" {
			if scheme == "" {
				scheme = "http"
				if r.TLS != nil {
					scheme = "https"
				}
			}
			return scheme + "://" + host + p.path
		}
	}
	return p.endpointURL()
}

func (p *Platform) endpointURL() string {
	if p.publicURL != "" {
		return strings.TrimRight(p.publicURL, "/") + p.path
	}
	host, port, err := net.SplitHostPort(p.listenAddr)
	if err != nil {
		return "http://localhost" + p.listenAddr + p.path
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + p.path
}

func forwardedURLParts(value string) (string, string) {
	value = firstHeaderValue(value)
	if value == "" {
		return "", ""
	}
	var scheme, host string
	for _, part := range strings.Split(value, ";") {
		key, raw, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		raw = strings.Trim(strings.TrimSpace(raw), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "proto":
			scheme = raw
		case "host":
			host = raw
		}
	}
	return scheme, host
}

func firstHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	first, _, _ := strings.Cut(value, ",")
	return strings.TrimSpace(first)
}

func (p *Platform) setHandler(handler core.MessageHandler) {
	p.handlerMu.Lock()
	p.handler = handler
	p.handlerMu.Unlock()
}

func (p *Platform) getHandler() core.MessageHandler {
	p.handlerMu.RLock()
	defer p.handlerMu.RUnlock()
	return p.handler
}

func (p *Platform) authInterceptor() a2asrv.CallInterceptor {
	return authInterceptor{token: p.apiToken}
}

func (p *Platform) completePending(taskID, content string) bool {
	if p.pending == nil {
		return false
	}
	return p.pending.complete(taskID, pendingResult{content: content})
}

type sdkExecutor struct {
	platform *Platform
}

type authInterceptor struct {
	token string
}

func (i authInterceptor) Before(ctx context.Context, callCtx *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	userName := serviceParamValue(callCtx, "X-A2A-User")
	if userName == "" {
		userName = "a2a"
	}
	if i.token == "" {
		callCtx.User = &a2asrv.User{Name: userName, Authenticated: false}
		return ctx, nil, nil
	}

	auth := serviceParamValue(callCtx, "Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || token != i.token {
		return ctx, nil, errUnauthorized
	}
	callCtx.User = a2asrv.NewAuthenticatedUser(userName, nil)
	return ctx, nil, nil
}

func (i authInterceptor) After(context.Context, *a2asrv.CallContext, *a2asrv.Response) error {
	return nil
}

func serviceParamValue(callCtx *a2asrv.CallContext, key string) string {
	if callCtx == nil || callCtx.ServiceParams() == nil {
		return ""
	}
	for _, candidate := range []string{key, strings.ToLower(key), http.CanonicalHeaderKey(key)} {
		if values, ok := callCtx.ServiceParams().Get(candidate); ok && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func (e *sdkExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[sdka2a.Event, error] {
	return func(yield func(sdka2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(sdka2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}

		waiter, ok := e.platform.pending.create(string(execCtx.TaskID))
		if !ok {
			yield(statusEvent(execCtx, sdka2a.TaskStateRejected, errors.New("a2a: too many pending tasks")), nil)
			return
		}
		defer e.platform.pending.delete(string(execCtx.TaskID))

		msg, err := e.platform.toCoreMessage(execCtx)
		if err != nil {
			yield(failedEvent(execCtx, err), nil)
			return
		}
		if !yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateWorking, nil), nil) {
			return
		}

		handler := e.platform.getHandler()
		if handler == nil {
			yield(failedEvent(execCtx, errors.New("platform handler is not ready")), nil)
			return
		}
		go handler(e.platform, &msg)

		select {
		case result := <-waiter.done:
			if result.state == sdka2a.TaskStateCanceled {
				yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateCanceled, nil), nil)
				return
			}
			if result.err != nil {
				yield(failedEvent(execCtx, result.err), nil)
				return
			}
			if result.content != "" {
				if !yield(sdka2a.NewArtifactEvent(execCtx, sdka2a.NewTextPart(result.content)), nil) {
					return
				}
			}
			yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateCompleted, nil), nil)
		case <-time.After(e.platform.timeout):
			yield(failedEvent(execCtx, fmt.Errorf("a2a: task timed out after %s", e.platform.timeout)), nil)
		case <-ctx.Done():
			yield(failedEvent(execCtx, ctx.Err()), nil)
		}
	}
}

func (e *sdkExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[sdka2a.Event, error] {
	return func(yield func(sdka2a.Event, error) bool) {
		e.platform.pending.cancel(string(execCtx.TaskID))
		yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateCanceled, nil), nil)
	}
}

func (p *Platform) toCoreMessage(execCtx *a2asrv.ExecutorContext) (core.Message, error) {
	if execCtx.Message == nil {
		return core.Message{}, errors.New("missing A2A message")
	}

	content, files, err := partsToCore(execCtx.Message.Parts)
	if err != nil {
		return core.Message{}, err
	}
	userID := "a2a"
	if execCtx.User != nil && execCtx.User.Name != "" {
		userID = execCtx.User.Name
	}
	if content == "" {
		content = "(empty A2A message)"
	}

	sessionKey := sessionKeyFor(execCtx)
	headers := collectForwardedHeaders(p.forwardHeaders, execCtx.ServiceParams)
	hookContext := collectHookContext(execCtx.Metadata, execCtx.Message.Metadata)
	return core.Message{
		SessionKey: sessionKey,
		Platform:   "a2a",
		MessageID:  execCtx.Message.ID,
		ChannelID:  string(execCtx.ContextID),
		ChannelKey: string(execCtx.ContextID),
		UserID:     userID,
		Content:    content,
		Files:      files,
		ReplyCtx: replyContext{
			taskID:     string(execCtx.TaskID),
			sessionKey: sessionKey,
			headers:    headers,
			context:    hookContext,
		},
	}, nil
}

func sessionKeyFor(execCtx *a2asrv.ExecutorContext) string {
	id := strings.TrimSpace(execCtx.ContextID)
	if id == "" {
		id = string(execCtx.TaskID)
	}
	if id == "" && execCtx.Message != nil {
		id = execCtx.Message.ID
	}
	if id == "" {
		id = "unknown"
	}
	return "a2a:" + id
}

func partsToCore(parts sdka2a.ContentParts) (string, []core.FileAttachment, error) {
	var text []string
	files := make([]core.FileAttachment, 0)

	for _, part := range parts {
		if part == nil {
			continue
		}
		if value := strings.TrimSpace(part.Text()); value != "" {
			text = append(text, value)
			continue
		}
		if data := part.Data(); data != nil {
			b, err := json.Marshal(data)
			if err != nil {
				return "", nil, fmt.Errorf("a2a: marshal data part: %w", err)
			}
			text = append(text, string(b))
			continue
		}
		if raw := part.Raw(); len(raw) > 0 {
			files = append(files, core.FileAttachment{
				FileName: part.Filename,
				MimeType: part.MediaType,
				Data:     raw,
			})
			continue
		}
		if fileURL := part.URL(); fileURL != "" {
			text = append(text, fmt.Sprintf("File URL: %s", fileURL))
		}
	}

	return strings.Join(text, "\n\n"), files, nil
}

func failedEvent(execCtx *a2asrv.ExecutorContext, err error) *sdka2a.TaskStatusUpdateEvent {
	return statusEvent(execCtx, sdka2a.TaskStateFailed, err)
}

func statusEvent(execCtx *a2asrv.ExecutorContext, state sdka2a.TaskState, err error) *sdka2a.TaskStatusUpdateEvent {
	if err == nil {
		return sdka2a.NewStatusUpdateEvent(execCtx, state, nil)
	}
	msg := sdka2a.NewMessageForTask(sdka2a.MessageRoleAgent, execCtx, sdka2a.NewTextPart(err.Error()))
	return sdka2a.NewStatusUpdateEvent(execCtx, state, msg)
}

type replyContext struct {
	taskID     string
	sessionKey string
	headers    map[string]string // whitelisted inbound HTTP headers (cc-connect only, not agent prompt)
	context    map[string]any    // merged A2A request/message metadata for cc-connect hooks
}

// HookContext returns whitelisted inbound headers and A2A metadata captured for
// this turn. The values are delivered only to cc-connect hooks, not to the agent
// prompt.
func (p *Platform) HookContext(replyCtx any) core.HookContext {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return core.HookContext{}
	}
	out := core.HookContext{}
	if len(rc.headers) > 0 {
		out.Headers = make(map[string]string, len(rc.headers))
		for k, v := range rc.headers {
			out.Headers[k] = v
		}
	}
	if len(rc.context) > 0 {
		out.Context = make(map[string]any, len(rc.context))
		for k, v := range rc.context {
			out.Context[k] = v
		}
	}
	return out
}

type streamingCard struct {
	platform *Platform
	taskID   string
}

func (c *streamingCard) Update(_ context.Context, _ string) error {
	return nil
}

func (c *streamingCard) Finalize(_ context.Context, content string) error {
	c.platform.completePending(c.taskID, content)
	return nil
}

func (c *streamingCard) Failed() bool {
	return false
}

type pendingStore struct {
	mu    sync.Mutex
	items map[string]*pendingTask
	max   int
	ttl   time.Duration
	now   func() time.Time
}

type pendingTask struct {
	once      sync.Once
	done      chan pendingResult
	createdAt time.Time
}

type pendingResult struct {
	content string
	err     error
	state   sdka2a.TaskState
}

func newPendingStore(max int, ttl time.Duration) *pendingStore {
	if max <= 0 {
		max = defaultMaxTasks
	}
	if ttl <= 0 {
		ttl = defaultTaskTTL
	}
	return &pendingStore{
		items: make(map[string]*pendingTask),
		max:   max,
		ttl:   ttl,
		now:   time.Now,
	}
}

func (s *pendingStore) create(taskID string) (*pendingTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now())
	if _, exists := s.items[taskID]; exists {
		return nil, false
	}
	if len(s.items) >= s.max {
		return nil, false
	}
	task := &pendingTask{done: make(chan pendingResult, 1), createdAt: s.now()}
	s.items[taskID] = task
	return task, true
}

func (s *pendingStore) get(taskID string) (*pendingTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(s.now())
	task, ok := s.items[taskID]
	return task, ok
}

func (s *pendingStore) complete(taskID string, result pendingResult) bool {
	s.mu.Lock()
	task := s.items[taskID]
	s.mu.Unlock()
	if task == nil {
		return false
	}

	completed := false
	task.once.Do(func() {
		task.done <- result
		close(task.done)
		completed = true
	})
	return completed
}

func (s *pendingStore) cancel(taskID string) {
	s.complete(taskID, pendingResult{err: context.Canceled, state: sdka2a.TaskStateCanceled})
}

func (s *pendingStore) delete(taskID string) {
	s.mu.Lock()
	delete(s.items, taskID)
	s.mu.Unlock()
}

func (s *pendingStore) cleanupLocked(now time.Time) {
	for id, task := range s.items {
		if now.Sub(task.createdAt) > s.ttl {
			task.once.Do(func() {
				task.done <- pendingResult{err: context.DeadlineExceeded, state: sdka2a.TaskStateFailed}
				close(task.done)
			})
			delete(s.items, id)
		}
	}
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return defaultPath
	}
	if strings.Contains(path, "://") {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

func skillsOption(opts map[string]any) ([]sdka2a.AgentSkill, error) {
	if opts == nil || opts["skills"] == nil {
		return defaultSkills(), nil
	}
	raw := opts["skills"]
	switch v := raw.(type) {
	case []sdka2a.AgentSkill:
		if len(v) == 0 {
			return nil, errors.New("a2a: skills must not be empty")
		}
		for i := range v {
			if err := validateSkill(v[i], i); err != nil {
				return nil, err
			}
			normalizeSkillSlices(&v[i])
		}
		return v, nil
	case []map[string]any:
		return parseSkillMaps(v)
	case []any:
		maps := make([]map[string]any, 0, len(v))
		for i, item := range v {
			switch skill := item.(type) {
			case map[string]any:
				maps = append(maps, skill)
			default:
				return nil, fmt.Errorf("a2a: skills[%d] must be a table, got %T", i, item)
			}
		}
		return parseSkillMaps(maps)
	default:
		return nil, fmt.Errorf("a2a: skills must be an array of tables, got %T", raw)
	}
}

func defaultSkills() []sdka2a.AgentSkill {
	return []sdka2a.AgentSkill{
		{
			ID:          "cc-connect",
			Name:        "Coding agent bridge",
			Description: "Forward A2A messages to the configured cc-connect coding agent.",
			Tags:        []string{"coding-agent", "automation", "bridge"},
		},
	}
}

func parseSkillMaps(raw []map[string]any) ([]sdka2a.AgentSkill, error) {
	if len(raw) == 0 {
		return nil, errors.New("a2a: skills must not be empty")
	}
	skills := make([]sdka2a.AgentSkill, 0, len(raw))
	for i, item := range raw {
		skill := sdka2a.AgentSkill{
			ID:          strings.TrimSpace(stringOption(item, "id", "")),
			Name:        strings.TrimSpace(stringOption(item, "name", "")),
			Description: strings.TrimSpace(stringOption(item, "description", "")),
			Tags:        stringSliceOption(item, "tags"),
			Examples:    stringSliceOption(item, "examples"),
			InputModes:  stringSliceOption(item, "input_modes"),
			OutputModes: stringSliceOption(item, "output_modes"),
		}
		if len(skill.InputModes) == 0 {
			skill.InputModes = stringSliceOption(item, "inputModes")
		}
		if len(skill.OutputModes) == 0 {
			skill.OutputModes = stringSliceOption(item, "outputModes")
		}
		if err := validateSkill(skill, i); err != nil {
			return nil, err
		}
		normalizeSkillSlices(&skill)
		skills = append(skills, skill)
	}
	return skills, nil
}

func validateSkill(skill sdka2a.AgentSkill, idx int) error {
	if strings.TrimSpace(skill.ID) == "" {
		return fmt.Errorf("a2a: skills[%d].id is required", idx)
	}
	if strings.TrimSpace(skill.Name) == "" {
		return fmt.Errorf("a2a: skills[%d].name is required", idx)
	}
	if strings.TrimSpace(skill.Description) == "" {
		return fmt.Errorf("a2a: skills[%d].description is required", idx)
	}
	return nil
}

func normalizeSkillSlices(skill *sdka2a.AgentSkill) {
	if skill.Tags == nil {
		skill.Tags = []string{}
	}
	if skill.Examples == nil {
		skill.Examples = []string{}
	}
	if skill.InputModes == nil {
		skill.InputModes = []string{}
	}
	if skill.OutputModes == nil {
		skill.OutputModes = []string{}
	}
}

func stringOption(opts map[string]any, key, fallback string) string {
	if opts == nil {
		return fallback
	}
	value, ok := opts[key]
	if !ok || value == nil {
		return fallback
	}
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	default:
		return fmt.Sprint(v)
	}
}

func stringSliceOption(opts map[string]any, key string) []string {
	if opts == nil || opts[key] == nil {
		return nil
	}
	switch v := opts[key].(type) {
	case []string:
		return cleanStringSlice(v)
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			value := strings.TrimSpace(fmt.Sprint(item))
			if value != "" {
				values = append(values, value)
			}
		}
		return values
	default:
		value := strings.TrimSpace(fmt.Sprint(v))
		if value == "" {
			return nil
		}
		return []string{value}
	}
}

func cleanStringSlice(in []string) []string {
	values := make([]string, 0, len(in))
	for _, item := range in {
		value := strings.TrimSpace(item)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func durationOption(opts map[string]any, key string, fallback time.Duration) (time.Duration, error) {
	if opts == nil {
		return fallback, nil
	}
	value, ok := opts[key]
	if !ok || value == nil {
		return fallback, nil
	}
	switch v := value.(type) {
	case time.Duration:
		return v, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback, nil
		}
		return time.ParseDuration(v)
	case int:
		return time.Duration(v) * time.Second, nil
	case int64:
		return time.Duration(v) * time.Second, nil
	case float64:
		return time.Duration(v * float64(time.Second)), nil
	default:
		return 0, fmt.Errorf("unsupported value type %T", value)
	}
}

func intOption(opts map[string]any, key string, fallback int) (int, error) {
	if opts == nil {
		return fallback, nil
	}
	value, ok := opts[key]
	if !ok || value == nil {
		return fallback, nil
	}
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback, nil
		}
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported value type %T", value)
	}
}

func init() {
	core.RegisterPlatform("a2a", New)
}

var _ core.Platform = (*Platform)(nil)
var _ core.StreamingCardPlatform = (*Platform)(nil)
var _ core.HookContextProvider = (*Platform)(nil)

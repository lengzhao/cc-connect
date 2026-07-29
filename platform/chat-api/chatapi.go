package chatapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	defaultListenAddr             = ":8030"
	defaultPath                   = "/v1/"
	defaultTimeout                = 30 * time.Minute
	autoGenerateNameModeHeuristic = "heuristic"
	autoGenerateNameModeAI        = "ai"
	defaultNameRequestTimeout     = 30 * time.Second
	defaultNameProviderType       = "openai"
	defaultQuestionNotifyTimeout  = 5 * time.Second
)

// Platform exposes a Dify-like HTTP + SSE API for custom apps and BFFs.
type Platform struct {
	listenAddr                string
	path                      string
	apiToken                  string
	userHeader                string
	userNameHeader            string
	channelHeader             string
	agentContextHeaders       agentContextHeaderMap
	forwardHeaders            []string
	corsOrigins               []string
	requestTimeout            time.Duration
	interactionTimeout        time.Duration
	ssePingInterval           time.Duration
	busyPolicy                string
	includeAnswerInMessageEnd bool
	autoGenerateNameMode      string
	nameProviderAPIKey        string
	nameProviderBaseURL       string
	nameProviderType          string
	nameModel                 string
	projectName               string
	sessionStorePath          string
	dataDir                   string
	multiWorkspaceBaseDir     string
	resolvedAddr              string
	debugUI                   bool
	questionNotifyURL         string
	questionNotifySecret      string
	questionNotifyHeaders     map[string]string
	questionNotifyTimeout     time.Duration
	toolSSETransforms         *toolSSETransformRegistry

	server   *http.Server
	handler  core.MessageHandler
	sessions *core.SessionManager
	pending  *pendingStore

	mu        sync.Mutex
	handlerMu sync.RWMutex
	running   bool
	cancel    context.CancelFunc
}

func New(opts map[string]any) (core.Platform, error) {
	listenAddr := stringOption(opts, "listen_addr", stringOption(opts, "listen", defaultListenAddr))
	path := normalizePath(stringOption(opts, "path", defaultPath))
	if path == "" {
		return nil, errors.New("chat-api: path must start and end with /")
	}

	timeout, err := durationOption(opts, "request_timeout", defaultTimeout)
	if err != nil {
		return nil, fmt.Errorf("chat-api: request_timeout: %w", err)
	}
	if _, ok := opts["request_timeout"]; !ok {
		timeout, err = durationOption(opts, "timeout", defaultTimeout)
		if err != nil {
			return nil, fmt.Errorf("chat-api: timeout: %w", err)
		}
	}

	interactionTimeout, err := durationOption(opts, "interaction_timeout", defaultInteractionTimeout)
	if err != nil {
		return nil, fmt.Errorf("chat-api: interaction_timeout: %w", err)
	}
	ssePingInterval, err := durationOptionAllowZero(opts, "sse_ping_interval", defaultSSEPingInterval)
	if err != nil {
		return nil, fmt.Errorf("chat-api: sse_ping_interval: %w", err)
	}

	maxRuns, err := intOption(opts, "max_runs", defaultMaxRuns)
	if err != nil {
		return nil, fmt.Errorf("chat-api: max_runs: %w", err)
	}
	questionNotifyTimeout, err := durationOption(opts, "question_notify_timeout", defaultQuestionNotifyTimeout)
	if err != nil {
		return nil, fmt.Errorf("chat-api: question_notify_timeout: %w", err)
	}

	userHeader, err := userHeaderOption(opts)
	if err != nil {
		return nil, err
	}
	userNameHeader, err := userNameHeaderOption(opts)
	if err != nil {
		return nil, err
	}
	channelHeader, err := channelHeaderOption(opts)
	if err != nil {
		return nil, err
	}
	agentContextHeaders, err := agentContextHeadersOption(opts)
	if err != nil {
		return nil, err
	}
	forwardHeaders := normalizeForwardHeaderNames(stringSliceOption(opts, "forward_headers"))

	busyPolicy := strings.ToLower(stringOption(opts, "busy_policy", busyPolicyQueue))
	if busyPolicy != busyPolicyQueue && busyPolicy != busyPolicyReject {
		return nil, errors.New("chat-api: busy_policy must be queue or reject")
	}

	p := &Platform{
		listenAddr:                listenAddr,
		path:                      path,
		apiToken:                  strings.TrimSpace(stringOption(opts, "api_token", stringOption(opts, "token", ""))),
		userHeader:                userHeader,
		userNameHeader:            userNameHeader,
		channelHeader:             channelHeader,
		agentContextHeaders:       agentContextHeaders,
		forwardHeaders:            forwardHeaders,
		corsOrigins:               stringSliceOption(opts, "cors_origins"),
		requestTimeout:            timeout,
		interactionTimeout:        interactionTimeout,
		ssePingInterval:           ssePingInterval,
		busyPolicy:                busyPolicy,
		includeAnswerInMessageEnd: boolOption(opts, "include_answer_in_message_end", false),
		autoGenerateNameMode:      strings.ToLower(stringOption(opts, "auto_generate_name_mode", autoGenerateNameModeHeuristic)),
		nameProviderAPIKey:        strings.TrimSpace(stringOption(opts, "name_provider_api_key", "")),
		nameProviderBaseURL:       strings.TrimSpace(stringOption(opts, "name_provider_base_url", "")),
		nameProviderType:          strings.ToLower(stringOption(opts, "name_provider_type", defaultNameProviderType)),
		nameModel:                 strings.TrimSpace(stringOption(opts, "name_model", "")),
		projectName:               stringOption(opts, "cc_project", ""),
		pending:                   newPendingStore(maxRuns),
		dataDir:                   stringOption(opts, "cc_data_dir", ""),
		multiWorkspaceBaseDir:     multiWorkspaceBaseDirFromOpts(opts),
		debugUI:                   boolOption(opts, "debug_ui", false),
		questionNotifyURL:         strings.TrimSpace(stringOption(opts, "question_notify_url", "")),
		questionNotifySecret:      stringOption(opts, "question_notify_secret", ""),
		questionNotifyHeaders:     stringStringMapOption(opts, "question_notify_headers"),
		questionNotifyTimeout:     questionNotifyTimeout,
	}
	if p.autoGenerateNameMode != autoGenerateNameModeHeuristic && p.autoGenerateNameMode != autoGenerateNameModeAI {
		return nil, errors.New("chat-api: auto_generate_name_mode must be heuristic or ai")
	}
	if p.nameProviderType != "openai" && p.nameProviderType != "openai-compatible" && p.nameProviderType != "claude" {
		return nil, errors.New("chat-api: name_provider_type must be openai, openai-compatible, or claude")
	}
	if p.autoGenerateNameMode == autoGenerateNameModeAI && p.nameModel != "" && p.nameProviderAPIKey == "" {
		slog.Warn("chat-api: name_model configured but provider credentials are unavailable; name generation will fall back to heuristic",
			"name_model", p.nameModel)
	}
	transforms, err := loadToolSSETransforms(stringOption(opts, "tool_sse_transforms_file", ""))
	if err != nil {
		return nil, err
	}
	p.toolSSETransforms = transforms
	p.sessionStorePath = sessionStorePathFromOpts(opts)
	return p, nil
}

func (p *Platform) Name() string {
	return "chat-api"
}

func (p *Platform) UseWorkspaceSessionStore() bool {
	return false
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
		return fmt.Errorf("chat-api: listen %s: %w", p.listenAddr, err)
	}
	p.resolvedAddr = ln.Addr().String()

	p.running = true
	go func() {
		<-serveCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := p.server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("chat-api: shutdown server", "error", err)
		}
	}()
	go func() {
		if err := p.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("chat-api: serve", "error", err)
		}
	}()

	slog.Info("chat-api: server started", "listen_addr", p.resolvedAddr, "path", p.path)
	if p.debugUI {
		slog.Info("chat-api: debug UI enabled", "url", "http://"+p.resolvedAddr+"/debug/")
	}
	return nil
}

// ResolvedBaseURL returns the HTTP base URL including API path prefix (e.g. http://127.0.0.1:54321/v1).
func (p *Platform) ResolvedBaseURL() string {
	p.mu.Lock()
	addr := p.resolvedAddr
	path := p.path
	p.mu.Unlock()
	if addr == "" {
		addr = strings.TrimPrefix(p.listenAddr, ":")
		if addr == "" {
			addr = "127.0.0.1" + p.listenAddr
		}
	}
	return "http://" + addr + strings.TrimSuffix(path, "/")
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
		return fmt.Errorf("chat-api: stop server: %w", err)
	}
	return nil
}

func (p *Platform) setHandler(handler core.MessageHandler) {
	p.handlerMu.Lock()
	defer p.handlerMu.Unlock()
	p.handler = handler
}

func (p *Platform) getHandler() core.MessageHandler {
	p.handlerMu.RLock()
	defer p.handlerMu.RUnlock()
	return p.handler
}

func (p *Platform) routes() http.Handler {
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return p.corsHTTP(p.authHTTP(h))
	}
	mux.HandleFunc(p.path+"conversations", wrap(p.handleConversations))
	mux.HandleFunc(p.path+"conversations/", wrap(p.handleConversationSub))
	mux.HandleFunc(p.path+"chat-messages", wrap(p.handleChatMessages))
	mux.HandleFunc(p.path+"runs/", wrap(p.handleRunRoutes))
	mux.HandleFunc(p.path+"conversations/messages/respond", wrap(p.handleCardRespond))
	p.registerDebugUI(mux)
	return mux
}

func init() {
	core.RegisterPlatform("chat-api", New)
}

var _ core.Platform = (*Platform)(nil)
var _ core.StreamingCardPlatform = (*Platform)(nil)
var _ core.InlineButtonSender = (*Platform)(nil)
var _ core.CardSender = (*Platform)(nil)
var _ core.HookContextProvider = (*Platform)(nil)
var _ core.ProcessingEndNotifier = (*Platform)(nil)
var _ core.SessionManagerBinder = (*Platform)(nil)
var _ core.WorkspaceSessionStorePolicy = (*Platform)(nil)
var _ core.ChannelNameResolver = (*Platform)(nil)
var _ core.AskUserQuestionHistoryRecorder = (*Platform)(nil)
var _ core.PreferAskUserButtons = (*Platform)(nil)
var _ core.AskQuestionSender = (*Platform)(nil)
var _ core.ClientFlowSender = (*Platform)(nil)

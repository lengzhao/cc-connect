package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const providerUserMaxLength = 64

// ProviderProxy is a lightweight local reverse proxy that rewrites
// incompatible Anthropic API fields for third-party providers.
//
// Some providers (e.g. SiliconFlow) don't support thinking.type "adaptive"
// sent by Claude Code 2.x. The proxy optionally rewrites the thinking field
// and normalizes provider fields before forwarding.
type ProviderProxy struct {
	targetURL        string
	thinkingOverride string
	listener         net.Listener
	server           *http.Server
	once             sync.Once
}

// NewProviderProxy creates and starts a local reverse proxy for the
// given upstream URL. thinkingOverride controls what thinking.type to
// rewrite "adaptive" to (e.g. "disabled" or "enabled").
// Returns the local URL to use as ANTHROPIC_BASE_URL.
func NewProviderProxy(targetURL, thinkingOverride string) (*ProviderProxy, string, error) {
	target, err := url.Parse(strings.TrimRight(targetURL, "/"))
	if err != nil {
		return nil, "", fmt.Errorf("providerproxy: parse target: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("providerproxy: listen: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = target.Host
	}
	proxy.FlushInterval = -1 // flush SSE events immediately

	override := thinkingOverride
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages") {
			rewriteProviderRequest(r, override)
		}
		proxy.ServeHTTP(w, r)
	})

	pp := &ProviderProxy{
		targetURL:        targetURL,
		thinkingOverride: thinkingOverride,
		listener:         listener,
		server: &http.Server{
			Handler:      mux,
			ReadTimeout:  10 * time.Minute,
			WriteTimeout: 10 * time.Minute,
		},
	}

	go func() {
		if err := pp.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("providerproxy: serve error", "error", err)
		}
	}()

	localURL := fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
	slog.Info("providerproxy: started", "target", targetURL, "local", localURL, "thinking", thinkingOverride)
	return pp, localURL, nil
}

// Close shuts down the proxy.
func (pp *ProviderProxy) Close() {
	pp.once.Do(func() {
		pp.server.Close()
	})
}

// Matches reports whether this proxy forwards to the requested provider
// endpoint with the same optional thinking behavior.
func (pp *ProviderProxy) Matches(targetURL, thinkingOverride string) bool {
	return pp != nil && pp.targetURL == targetURL && pp.thinkingOverride == thinkingOverride
}

// rewriteProviderRequest keeps third-party Anthropic-compatible requests
// within provider limits. It preserves the original body when nothing needs
// changing.
func rewriteProviderRequest(r *http.Request, override string) {
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return
	}

	modified := normalizeProviderUser(data)
	if override != "" {
		if thinking, ok := data["thinking"].(map[string]any); ok {
			if t, ok := thinking["type"].(string); ok && t == "adaptive" {
				thinking["type"] = override
				if override == "disabled" {
					delete(thinking, "budget_tokens")
				}
				modified = true
				slog.Debug("providerproxy: rewrote thinking adaptive →", "override", override)
			}
		}
	}

	if !modified {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return
	}

	newBody, err := json.Marshal(data)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(newBody))
	r.ContentLength = int64(len(newBody))
}

func normalizeProviderUser(data map[string]any) bool {
	value, ok := data["user"].(string)
	if !ok || len(value) <= providerUserMaxLength {
		return false
	}
	sum := sha256.Sum256([]byte(value))
	data["user"] = "ccu_" + hex.EncodeToString(sum[:])[:providerUserMaxLength-len("ccu_")]
	return true
}

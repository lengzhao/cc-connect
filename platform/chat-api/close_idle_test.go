package chatapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type stubIdleCloser struct {
	result core.CloseIdleAgentSessionsResult
}

func (s *stubIdleCloser) CloseIdleAgentSessions() core.CloseIdleAgentSessionsResult {
	return s.result
}

func TestCloseIdleAgentSessions_OK(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"api_token": "secret"})
	p.BindIdleAgentSessionCloser(&stubIdleCloser{
		result: core.CloseIdleAgentSessionsResult{
			Closed:             2,
			Skipped:            1,
			ClosedSessionKeys:  []string{"a", "b"},
			SkippedSessionKeys: []string{"c"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/agent-sessions/close-idle", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Chat-API-User", "ops_user")
	req.Header.Set("X-Chat-API-Channel", testChannel)
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Closed             int      `json:"closed"`
			Skipped            int      `json:"skipped"`
			ClosedSessionKeys  []string `json:"closed_session_keys"`
			SkippedSessionKeys []string `json:"skipped_session_keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("ok = false, body = %s", rec.Body.String())
	}
	if resp.Data.Closed != 2 || resp.Data.Skipped != 1 {
		t.Fatalf("closed/skipped = %d/%d, want 2/1", resp.Data.Closed, resp.Data.Skipped)
	}
	if len(resp.Data.ClosedSessionKeys) != 2 || resp.Data.ClosedSessionKeys[0] != "a" || resp.Data.ClosedSessionKeys[1] != "b" {
		t.Fatalf("closed_session_keys = %#v", resp.Data.ClosedSessionKeys)
	}
	if len(resp.Data.SkippedSessionKeys) != 1 || resp.Data.SkippedSessionKeys[0] != "c" {
		t.Fatalf("skipped_session_keys = %#v", resp.Data.SkippedSessionKeys)
	}
}

func TestCloseIdleAgentSessions_Unbound_503(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"api_token": "secret"})

	req := httptest.NewRequest(http.MethodPost, "/v1/agent-sessions/close-idle", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatal("ok = true, want false")
	}
}

func TestCloseIdleAgentSessions_MethodNotAllowed(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"api_token": "secret"})
	p.BindIdleAgentSessionCloser(&stubIdleCloser{})

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-sessions/close-idle", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloseIdleAgentSessions_NoChannelRequired(t *testing.T) {
	p := newTestPlatform(t, map[string]any{"api_token": "secret"})
	p.BindIdleAgentSessionCloser(&stubIdleCloser{
		result: core.CloseIdleAgentSessionsResult{
			Closed:  0,
			Skipped: 0,
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/agent-sessions/close-idle", nil)
	req.Header.Set("Authorization", "Bearer secret")
	// Intentionally omit X-Chat-API-Channel and X-Chat-API-User.
	rec := httptest.NewRecorder()
	p.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 without channel header, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("ok = false, body = %s", rec.Body.String())
	}
}

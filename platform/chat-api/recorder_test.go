package chatapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
)

// safeResponseRecorder wraps httptest.ResponseRecorder with a mutex: SSE
// handlers stream on a separate goroutine while tests poll the body, and
// httptest.ResponseRecorder (bytes.Buffer) is not safe for that.
type safeResponseRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSafeResponseRecorder() *safeResponseRecorder {
	return &safeResponseRecorder{rec: httptest.NewRecorder()}
}

func (s *safeResponseRecorder) Header() http.Header { return s.rec.Header() }

func (s *safeResponseRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Write(p)
}

func (s *safeResponseRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.WriteHeader(code)
}

// Flush implements http.Flusher so SSE handlers can flush through the wrapper.
func (s *safeResponseRecorder) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.Flush()
}

// Body returns a snapshot copy; safe to read while the handler streams.
func (s *safeResponseRecorder) Body() *bytes.Buffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.NewBufferString(s.rec.Body.String())
}

// Code returns the recorded status code.
func (s *safeResponseRecorder) Code() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Code
}

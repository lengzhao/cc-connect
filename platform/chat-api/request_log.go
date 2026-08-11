package chatapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const maxLogRequestBodyBytes = 300

var httpRequestSeq atomic.Uint64

func (p *Platform) loggingHTTP(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := nextHTTPRequestID()

		reqBody, err := readRequestBodyForLog(r, maxLogRequestBodyBytes)
		if err != nil {
			slog.Warn("chat-api: http request body read",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"error", err,
			)
		}

		slog.Info("chat-api: http request",
			append([]any{
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"query", r.URL.RawQuery,
				"remote_addr", r.RemoteAddr,
				"content_length", r.ContentLength,
				"body", reqBody,
			}, p.httpLogContextAttrs(r)...)...,
		)

		lw := &loggingResponseWriter{
			ResponseWriter: w,
			status:         http.StatusOK,
		}
		next(lw, r)

		slog.Info("chat-api: http response",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", lw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

func nextHTTPRequestID() string {
	return strconv.FormatUint(httpRequestSeq.Add(1), 36)
}

func readRequestBodyForLog(r *http.Request, maxLog int) (string, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return "", nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return truncateHTTPBodyForLog(string(body), maxLog), nil
}

func truncateHTTPBodyForLog(body string, max int) string {
	if max <= 0 || len(body) <= max {
		return body
	}
	return body[:max] + "...(truncated)"
}

func (p *Platform) httpLogContextAttrs(r *http.Request) []any {
	if r == nil {
		return nil
	}
	var attrs []any

	if user := strings.TrimSpace(r.URL.Query().Get("user")); user != "" {
		attrs = append(attrs, "user", user)
	} else if v := headerValue(r, p.userHeader); v != "" {
		attrs = append(attrs, "user", v)
	}
	if v := headerValue(r, p.channelHeader); v != "" {
		attrs = append(attrs, "channel", v)
	}
	if header := p.agentContextHeaders["trace_id"]; header != "" {
		if v := headerValue(r, header); v != "" {
			attrs = append(attrs, "trace_id", v)
		}
	}
	if header := p.agentContextHeaders["task_id"]; header != "" {
		if v := headerValue(r, header); v != "" {
			attrs = append(attrs, "task", v)
		}
	}
	return attrs
}

func headerValue(r *http.Request, headerName string) string {
	if r == nil || headerName == "" {
		return ""
	}
	return strings.TrimSpace(r.Header.Get(headerName))
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *loggingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

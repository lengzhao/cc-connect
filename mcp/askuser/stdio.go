package askuser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type stdioFraming int

const (
	stdioFramingLine stdioFraming = iota
	stdioFramingHeader
)

// ServeStdio exposes the ask-user MCP tools over stdio and forwards calls to
// the resident daemon endpoint through a Unix socket.
func ServeStdio(ctx context.Context, in io.Reader, out io.Writer, socketPath, sessionKey string) error {
	if strings.TrimSpace(socketPath) == "" {
		return fmt.Errorf("askuser: socket path required")
	}
	if strings.TrimSpace(sessionKey) == "" {
		return fmt.Errorf("askuser: session key required")
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	br := bufio.NewReader(in)
	for {
		msg, framing, err := readStdioMessage(br)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			resp := stdioRPCError(nil, -32700, err.Error())
			if writeErr := writeStdioMessage(out, stdioFramingLine, resp); writeErr != nil {
				return writeErr
			}
			continue
		}
		resp, ok, err := proxyStdioMessage(ctx, client, msg, sessionKey)
		if err != nil {
			id := stdioRequestID(msg)
			resp = stdioRPCError(id, -32000, err.Error())
			ok = true
		}
		if !ok {
			continue
		}
		if err := writeStdioMessage(out, framing, resp); err != nil {
			return err
		}
	}
}

func readStdioMessage(br *bufio.Reader) ([]byte, stdioFraming, error) {
	for {
		b, err := br.Peek(1)
		if err != nil {
			return nil, stdioFramingLine, err
		}
		if !bytes.ContainsAny(b, " \t\r\n") {
			break
		}
		_, _ = br.ReadByte()
	}
	if hasHeaderPrefix(br) {
		return readHeaderFramedMessage(br)
	}
	line, err := br.ReadBytes('\n')
	if err != nil {
		if err == io.EOF && len(bytes.TrimSpace(line)) > 0 {
			return bytes.TrimSpace(line), stdioFramingLine, nil
		}
		return nil, stdioFramingLine, err
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return readStdioMessage(br)
	}
	return line, stdioFramingLine, nil
}

func hasHeaderPrefix(br *bufio.Reader) bool {
	const prefix = "Content-Length:"
	b, err := br.Peek(len(prefix))
	return err == nil && strings.EqualFold(string(b), prefix)
}

func readHeaderFramedMessage(br *bufio.Reader) ([]byte, stdioFraming, error) {
	contentLength := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, stdioFramingHeader, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, stdioFramingHeader, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, stdioFramingHeader, fmt.Errorf("missing Content-Length")
	}
	msg := make([]byte, contentLength)
	if _, err := io.ReadFull(br, msg); err != nil {
		return nil, stdioFramingHeader, err
	}
	return msg, stdioFramingHeader, nil
}

func proxyStdioMessage(ctx context.Context, client *http.Client, msg []byte, sessionKey string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, unixMCPHost+"/mcp", bytes.NewReader(msg))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(core.SessionKeyHeader, sessionKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(body)) == 0 {
		return nil, false, nil
	}
	if resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("daemon MCP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return bytes.TrimSpace(body), true, nil
}

func writeStdioMessage(out io.Writer, framing stdioFraming, msg []byte) error {
	if framing == stdioFramingHeader {
		if _, err := fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", len(msg)); err != nil {
			return err
		}
		_, err := out.Write(msg)
		return err
	}
	if _, err := out.Write(msg); err != nil {
		return err
	}
	_, err := out.Write([]byte("\n"))
	return err
}

func stdioRequestID(msg []byte) json.RawMessage {
	var req rpcRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		return nil
	}
	return req.ID
}

func stdioRPCError(id json.RawMessage, code int, message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRawOrNull(id),
		"error":   map[string]any{"code": code, "message": message},
	})
	return b
}

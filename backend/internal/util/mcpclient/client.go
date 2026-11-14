// Package mcpclient is a minimal Model Context Protocol client: initialize,
// tools/list and tools/call over a local process's stdio or over streamable
// HTTP. It is what LeapMux's own privileged components use to reach an MCP
// server; agents configure their MCP servers through their providers.
package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// ProtocolVersion is the MCP revision this client speaks.
const ProtocolVersion = "2025-06-18"

// Tool is one entry of tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations *struct {
		ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
		DestructiveHint *bool `json:"destructiveHint,omitempty"`
	} `json:"annotations,omitempty"`
}

// ReadOnly reports what the server claims about the tool: true only when it
// says readOnlyHint. A claim is advice, never a permission.
func (t Tool) ReadOnly() bool {
	return t.Annotations != nil && t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint
}

// Content is one item of a tool result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallResult is a tools/call result.
type CallResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// JSON returns the result's data: the structured content when present, else
// the first text item that parses as JSON.
func (r *CallResult) JSON() (json.RawMessage, error) {
	if len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		return r.StructuredContent, nil
	}
	for _, c := range r.Content {
		t := strings.TrimSpace(c.Text)
		if c.Type == "text" && json.Valid([]byte(t)) {
			return json.RawMessage(t), nil
		}
	}
	return nil, errors.New("the tool returned no JSON")
}

// Text joins the result's text items.
func (r *CallResult) Text() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// RPCError is an error the server answered with.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// transport sends one request and returns its response, or sends a
// notification when id is nil.
type transport interface {
	roundTrip(ctx context.Context, m *message) (*message, error)
	close() error
}

// Client is an initialized session with one server.
type Client struct {
	t      transport
	nextID atomic.Int64
	// Server is what initialize reported.
	Server struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
}

// Config chooses the transport.
type Config struct {
	// Command starts a local server speaking MCP over stdio.
	Command string
	Args    []string
	Env     []string
	// URL is a streamable-HTTP endpoint.
	URL string
	// BearerToken authorizes HTTP requests. It never appears in an error.
	BearerToken string
	// TokenSource, when set, supplies the bearer token for each HTTP request
	// instead, so a token it refreshes takes effect on the next call.
	TokenSource func(context.Context) (string, error)
	HTTPClient  *http.Client
	ClientName  string
}

// Dial connects and initializes.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	var t transport
	switch {
	case cfg.URL != "":
		hc := cfg.HTTPClient
		if hc == nil {
			hc = http.DefaultClient
		}
		t = &httpTransport{url: cfg.URL, token: cfg.BearerToken, tokens: cfg.TokenSource, hc: hc}
	case cfg.Command != "":
		st, err := startStdio(cfg.Command, cfg.Args, cfg.Env)
		if err != nil {
			return nil, err
		}
		t = st
	default:
		return nil, errors.New("mcp: neither a command nor a URL is configured")
	}
	c := &Client{t: t}
	name := cfg.ClientName
	if name == "" {
		name = "leapmux"
	}
	var init struct {
		ServerInfo json.RawMessage `json:"serverInfo"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": name, "version": "1"},
	}, &init); err != nil {
		_ = t.close()
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	_ = json.Unmarshal(init.ServerInfo, &c.Server)
	if _, err := t.roundTrip(ctx, &message{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		_ = t.close()
		return nil, fmt.Errorf("mcp initialized: %w", err)
	}
	return c, nil
}

// Close ends the session.
func (c *Client) Close() error { return c.t.close() }

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	resp, err := c.t.roundTrip(ctx, &message{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

// ListTools returns every tool, following pagination.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for range 50 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
	return nil, errors.New("mcp: tools/list did not end")
}

// CallTool invokes one tool. A result with isError is returned as an error
// carrying the tool's text.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	var res CallResult
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return nil, err
	}
	if res.IsError {
		return &res, fmt.Errorf("tool %s failed: %s", name, strings.TrimSpace(res.Text()))
	}
	return &res, nil
}

// --- HTTP ---

type httpTransport struct {
	url, token string
	tokens     func(context.Context) (string, error)
	hc         *http.Client
	mu         sync.Mutex
	session    string
}

func (h *httpTransport) roundTrip(ctx context.Context, m *message) (*message, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	token := h.token
	if h.tokens != nil {
		if token, err = h.tokens(ctx); err != nil {
			return nil, err
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.mu.Lock()
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	h.mu.Unlock()
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		h.mu.Lock()
		h.session = s
		h.mu.Unlock()
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: the server answered %s", ErrUnauthorized, resp.Status)
	case resp.StatusCode >= 300:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mcp: the server answered %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	if m.ID == nil {
		return nil, nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSE(resp.Body, *m.ID)
	}
	var out message
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMessageBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("mcp: unreadable response: %w", err)
	}
	return &out, nil
}

// ErrUnauthorized is the server refusing the credential.
var ErrUnauthorized = errors.New("mcp: not authorized")

const maxMessageBytes = 32 << 20

func readSSE(r io.Reader, id int64) (*message, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxMessageBytes)
	var data strings.Builder
	flush := func() (*message, bool) {
		defer data.Reset()
		if data.Len() == 0 {
			return nil, false
		}
		var m message
		if json.Unmarshal([]byte(data.String()), &m) != nil || m.ID == nil || *m.ID != id {
			return nil, false
		}
		return &m, true
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if m, ok := flush(); ok {
				return m, nil
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(v, " "))
		}
	}
	if m, ok := flush(); ok {
		return m, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("mcp: the event stream ended without a response")
}

func (h *httpTransport) close() error { return nil }

// --- stdio ---

type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex // one request at a time
	lines   chan []byte
	readErr atomic.Pointer[error]
}

func startStdio(command string, args, env []string) (*stdioTransport, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", command, err)
	}
	t := &stdioTransport{cmd: cmd, stdin: stdin, lines: make(chan []byte, 16)}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), maxMessageBytes)
		for sc.Scan() {
			t.lines <- bytes.Clone(sc.Bytes())
		}
		err := sc.Err()
		if err == nil {
			err = io.EOF
		}
		t.readErr.Store(&err)
		close(t.lines)
	}()
	return t, nil
}

func (s *stdioTransport) roundTrip(ctx context.Context, m *message) (*message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if _, err := s.stdin.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("mcp: write: %w", err)
	}
	if m.ID == nil {
		return nil, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case raw, ok := <-s.lines:
			if !ok {
				if e := s.readErr.Load(); e != nil {
					return nil, fmt.Errorf("mcp: the server closed its output: %w", *e)
				}
				return nil, errors.New("mcp: the server closed its output")
			}
			var resp message
			if json.Unmarshal(raw, &resp) != nil {
				continue // a log line or a notification
			}
			if resp.Method != "" && resp.ID != nil {
				// A request from the server (ping and the like): answer it
				// empty so it does not wait on us.
				reply, _ := json.Marshal(message{JSONRPC: "2.0", ID: resp.ID, Result: json.RawMessage(`{}`)})
				_, _ = s.stdin.Write(append(reply, '\n'))
				continue
			}
			if resp.ID != nil && *resp.ID == *m.ID {
				return &resp, nil
			}
		}
	}
}

func (s *stdioTransport) close() error {
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
	return nil
}

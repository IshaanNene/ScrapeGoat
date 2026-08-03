package mcp

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Transport defines how the MCP server communicates with clients.
type Transport interface {
	// Run starts the transport and blocks until ctx is cancelled.
	// handler is called for each incoming JSON-RPC message.
	Run(ctx context.Context, handler MessageHandler) error
}

// MessageHandler processes a raw JSON-RPC message and returns the response bytes.
type MessageHandler func(ctx context.Context, message []byte) ([]byte, error)

// --- Stdio Transport ---

// StdioTransport reads/writes JSON-RPC messages over stdin/stdout.
// This is the primary transport for Claude Desktop and Cursor integration.
type StdioTransport struct {
	logger *slog.Logger
	reader io.Reader
	writer io.Writer
}

// NewStdioTransport creates a stdio transport using os.Stdin and os.Stdout.
func NewStdioTransport(logger *slog.Logger) *StdioTransport {
	return &StdioTransport{
		logger: logger.With("transport", "stdio"),
		reader: os.Stdin,
		writer: os.Stdout,
	}
}

// NewStdioTransportWithIO creates a stdio transport with custom reader/writer (for testing).
func NewStdioTransportWithIO(logger *slog.Logger, reader io.Reader, writer io.Writer) *StdioTransport {
	return &StdioTransport{
		logger: logger.With("transport", "stdio"),
		reader: reader,
		writer: writer,
	}
}

// Run reads newline-delimited JSON from stdin, processes each message, and writes
// the response to stdout. Blocks until ctx is cancelled or EOF.
func (t *StdioTransport) Run(ctx context.Context, handler MessageHandler) error {
	t.logger.Info("stdio transport started")
	scanner := bufio.NewScanner(t.reader)
	// Allow up to 10MB per message.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		resp, err := handler(ctx, line)
		if err != nil {
			t.logger.Error("handler error", "error", err)
			continue
		}
		if resp == nil {
			// Notification — no response needed.
			continue
		}

		// Write response followed by newline.
		if _, err := fmt.Fprintf(t.writer, "%s\n", resp); err != nil {
			t.logger.Error("write error", "error", err)
			return fmt.Errorf("write response: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

// --- HTTP/SSE Transport ---

// HTTPTransport serves MCP over HTTP with SSE streaming support.
// HTTP transport requires API key authentication.
type HTTPTransport struct {
	port    int
	apiKey  string
	logger  *slog.Logger
	handler MessageHandler
	clients map[string]chan []byte // SSE clients by session
	mu      sync.RWMutex
}

// NewHTTPTransport creates an HTTP/SSE transport.
func NewHTTPTransport(port int, apiKey string, logger *slog.Logger) *HTTPTransport {
	return &HTTPTransport{
		port:    port,
		apiKey:  apiKey,
		logger:  logger.With("transport", "http"),
		clients: make(map[string]chan []byte),
	}
}

// ErrNoAPIKey is returned when the HTTP transport is started without an API key.
var ErrNoAPIKey = errors.New("mcp http transport requires an API key")

// Run starts the HTTP server and blocks until ctx is cancelled.
//
// It refuses to start without an API key. The transport's own documentation already
// said HTTP requires authentication, but an empty key silently authorised every
// request — and these tools fetch arbitrary URLs on the caller's behalf.
func (t *HTTPTransport) Run(ctx context.Context, handler MessageHandler) error {
	if t.apiKey == "" {
		return fmt.Errorf("%w: pass --api-key or set SCRAPEGOAT_MCP_API_KEY "+
			"(the stdio transport needs no key)", ErrNoAPIKey)
	}

	t.handler = handler

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", t.handleMCP)
	mux.HandleFunc("/mcp/sse", t.handleSSE)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	addr := fmt.Sprintf(":%d", t.port)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		t.logger.Info("HTTP transport started", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// authenticate checks the API key on HTTP requests.
//
// An empty configured key denies rather than allows: Run refuses to start without
// one, so reaching here with no key means something constructed a transport out of
// band, and the safe reading of that is "not authorised".
func (t *HTTPTransport) authenticate(r *http.Request) bool {
	if t.apiKey == "" {
		return false
	}
	key := r.Header.Get("X-API-Key")
	if key == "" {
		key = r.Header.Get("Authorization")
		key = strings.TrimPrefix(key, "Bearer ")
	}
	// Constant-time, so response latency does not leak the key byte by byte.
	return subtle.ConstantTimeCompare([]byte(key), []byte(t.apiKey)) == 1
}

func (t *HTTPTransport) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	if !t.authenticate(r) {
		http.Error(w, `{"error":"unauthorized","message":"valid API key required via X-API-Key header"}`, http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, `{"error":"read body failed"}`, http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	resp, err := t.handler(r.Context(), body)
	if err != nil {
		t.logger.Error("handler error", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

func (t *HTTPTransport) handleSSE(w http.ResponseWriter, r *http.Request) {
	if !t.authenticate(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// No Access-Control-Allow-Origin: this stream is for a local MCP client, not for
	// a web page. Advertising it as cross-origin readable only widens the target.

	// Create a unique session ID for this SSE client.
	sessionID := fmt.Sprintf("sse-%d", time.Now().UnixNano())
	ch := make(chan []byte, 100)

	t.mu.Lock()
	t.clients[sessionID] = ch
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.clients, sessionID)
		t.mu.Unlock()
		close(ch)
	}()

	// Send the endpoint URL for the client to POST to.
	fmt.Fprintf(w, "event: endpoint\ndata: /mcp\n\n")
	flusher.Flush()

	// Stream events.
	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// BroadcastSSE sends a message to all connected SSE clients.
func (t *HTTPTransport) BroadcastSSE(data []byte) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, ch := range t.clients {
		select {
		case ch <- data:
		default:
			// Client channel full, skip.
		}
	}
}

// SSEEvent creates a JSON-encoded SSE event.
func SSEEvent(eventType string, data any) ([]byte, error) {
	payload := map[string]any{
		"type":      eventType,
		"data":      data,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestServer_Initialize(t *testing.T) {
	server := NewServer(testLogger(), "")
	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test-client","version":"1.0"}}}`

	resp, err := server.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}

	var result JSONRPCMessage
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	var initResult InitializeResult
	if err := json.Unmarshal(result.Result, &initResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if initResult.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol version = %q, want %q", initResult.ProtocolVersion, ProtocolVersion)
	}
	if initResult.ServerInfo.Name != "scrapegoat" {
		t.Errorf("server name = %q, want %q", initResult.ServerInfo.Name, "scrapegoat")
	}
}

func TestServer_ToolsList(t *testing.T) {
	server := NewServer(testLogger(), "")
	msg := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`

	resp, err := server.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}

	var result JSONRPCMessage
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	var toolList ToolListResult
	if err := json.Unmarshal(result.Result, &toolList); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	expectedTools := []string{
		"scrapegoat_crawl",
		"scrapegoat_extract",
		"scrapegoat_search",
		"scrapegoat_screenshot",
		"scrapegoat_batch",
		"scrapegoat_job_status",
		"scrapegoat_sitemap",
		"scrapegoat_seo_audit",
	}

	if len(toolList.Tools) != len(expectedTools) {
		t.Fatalf("tool count = %d, want %d", len(toolList.Tools), len(expectedTools))
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolList.Tools {
		toolNames[tool.Name] = true
	}

	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestServer_Ping(t *testing.T) {
	server := NewServer(testLogger(), "")
	msg := `{"jsonrpc":"2.0","id":3,"method":"ping"}`

	resp, err := server.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}

	var result JSONRPCMessage
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if result.Error != nil {
		t.Fatalf("ping should not return error: %v", result.Error)
	}
}

func TestServer_MethodNotFound(t *testing.T) {
	server := NewServer(testLogger(), "")
	msg := `{"jsonrpc":"2.0","id":4,"method":"nonexistent/method"}`

	resp, err := server.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}

	var result JSONRPCMessage
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if result.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if result.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("error code = %d, want %d", result.Error.Code, ErrCodeMethodNotFound)
	}
}

func TestServer_InvalidJSON(t *testing.T) {
	server := NewServer(testLogger(), "")
	resp, err := server.HandleMessage(context.Background(), []byte("not json"))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}

	var result JSONRPCMessage
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if result.Error == nil {
		t.Fatal("expected parse error")
	}
	if result.Error.Code != ErrCodeParseError {
		t.Errorf("error code = %d, want %d", result.Error.Code, ErrCodeParseError)
	}
}

func TestServer_Notification(t *testing.T) {
	server := NewServer(testLogger(), "")
	msg := `{"jsonrpc":"2.0","method":"notifications/initialized"}`

	resp, err := server.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}

	if resp != nil {
		t.Errorf("notification should return nil response, got: %s", resp)
	}
}

func TestServer_ToolCallUnknownTool(t *testing.T) {
	server := NewServer(testLogger(), "")
	msg := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"unknown_tool","arguments":{}}}`

	resp, err := server.HandleMessage(context.Background(), []byte(msg))
	if err != nil {
		t.Fatalf("HandleMessage error: %v", err)
	}

	var result JSONRPCMessage
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Tool not found is returned as a tool result with isError=true.
	var toolResult ToolCallResult
	if err := json.Unmarshal(result.Result, &toolResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !toolResult.IsError {
		t.Error("expected isError=true for unknown tool")
	}
}

func TestServer_JobLifecycle(t *testing.T) {
	server := NewServer(testLogger(), "")

	// Create a job.
	job := server.CreateJob()
	if job.ID == "" {
		t.Fatal("job ID should not be empty")
	}
	if job.Status != "pending" {
		t.Errorf("job status = %q, want %q", job.Status, "pending")
	}

	// Add items.
	job.AddItem(map[string]any{"title": "Test Item 1"})
	job.AddItem(map[string]any{"title": "Test Item 2"})

	items := job.GetItems()
	if len(items) != 2 {
		t.Errorf("item count = %d, want 2", len(items))
	}

	// Retrieve job.
	retrieved, ok := server.GetJob(job.ID)
	if !ok {
		t.Fatal("job not found")
	}
	if retrieved.ID != job.ID {
		t.Errorf("retrieved job ID = %q, want %q", retrieved.ID, job.ID)
	}

	// Non-existent job.
	_, ok = server.GetJob("nonexistent")
	if ok {
		t.Error("expected false for nonexistent job")
	}
}

func TestStdioTransport(t *testing.T) {
	logger := testLogger()
	server := NewServer(logger, "")

	// Prepare input: two messages.
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"

	reader := strings.NewReader(input)
	var output bytes.Buffer

	transport := NewStdioTransportWithIO(logger, reader, &output)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := transport.Run(ctx, server.HandleMessage)
	if err != nil {
		t.Fatalf("transport run error: %v", err)
	}

	// Parse output lines.
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 response lines, got %d: %s", len(lines), output.String())
	}

	// Verify first response is a ping.
	var resp1 JSONRPCMessage
	if err := json.Unmarshal([]byte(lines[0]), &resp1); err != nil {
		t.Fatalf("unmarshal response 1: %v", err)
	}
	if resp1.Error != nil {
		t.Errorf("response 1 error: %v", resp1.Error)
	}
}

func TestHTTPTransport_Auth(t *testing.T) {
	tests := []struct {
		name       string
		apiKey     string
		reqKey     string
		wantStatus int
	}{
		{
			name:       "valid key",
			apiKey:     "test-key-123",
			reqKey:     "test-key-123",
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid key",
			apiKey:     "test-key-123",
			reqKey:     "wrong-key",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing key",
			apiKey:     "test-key-123",
			reqKey:     "",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := testLogger()
			server := NewServer(logger, tt.apiKey)

			transport := NewHTTPTransport(0, tt.apiKey, logger)
			transport.handler = server.HandleMessage

			body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
			if tt.reqKey != "" {
				req.Header.Set("X-API-Key", tt.reqKey)
			}

			w := httptest.NewRecorder()
			transport.handleMCP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestHTTPTransport_MethodNotAllowed(t *testing.T) {
	logger := testLogger()
	transport := NewHTTPTransport(0, "", logger)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	transport.handleMCP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestToolDefinitions_ValidJSON(t *testing.T) {
	registry := NewToolRegistry(NewServer(testLogger(), ""), testLogger())
	tools := registry.List()

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			// Verify the name is non-empty.
			if tool.Name == "" {
				t.Error("tool name is empty")
			}
			if tool.Description == "" {
				t.Errorf("tool %s has empty description", tool.Name)
			}

			// Verify the input schema is valid JSON.
			var schema map[string]any
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Errorf("tool %s has invalid input schema JSON: %v", tool.Name, err)
			}

			// Verify the schema has a "type" field.
			if _, ok := schema["type"]; !ok {
				t.Errorf("tool %s input schema missing 'type' field", tool.Name)
			}

			// Verify there are required fields.
			if _, ok := schema["required"]; !ok {
				t.Errorf("tool %s input schema missing 'required' field", tool.Name)
			}
		})
	}
}

func TestMemoryCollector(t *testing.T) {
	collector := &memoryCollector{}

	// Store items.
	items := []*types.Item{
		{Fields: map[string]any{"title": "Item 1"}, URL: "https://example.com/1", Timestamp: time.Now()},
		{Fields: map[string]any{"title": "Item 2"}, URL: "https://example.com/2", Timestamp: time.Now()},
	}
	if err := collector.Store(items); err != nil {
		t.Fatalf("store error: %v", err)
	}

	collected := collector.Items()
	if len(collected) != 2 {
		t.Fatalf("item count = %d, want 2", len(collected))
	}

	if collected[0]["title"] != "Item 1" {
		t.Errorf("item 0 title = %v, want %q", collected[0]["title"], "Item 1")
	}

	if collector.Name() != "memory" {
		t.Errorf("name = %q, want %q", collector.Name(), "memory")
	}
	if err := collector.Close(); err != nil {
		t.Errorf("close error: %v", err)
	}
}

// Ensure types import is used.
var _ = (*types.Item)(nil)


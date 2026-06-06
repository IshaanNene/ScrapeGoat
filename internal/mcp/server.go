// Package mcp implements a Model Context Protocol (MCP) server that exposes
// ScrapeGoat's crawling, extraction, and SEO tools to AI agents such as
// Claude, GPT-4, Cursor, and Cline via JSON-RPC 2.0 over stdio or HTTP/SSE.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ProtocolVersion is the MCP protocol version supported by this server.
const ProtocolVersion = "2024-11-05"

// ServerInfo describes this MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// JSONRPCMessage is the base JSON-RPC 2.0 message envelope.
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
)

// InitializeParams is the client's initialize request payload.
type InitializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ClientInfo      ClientInfo `json:"clientInfo"`
}

// ClientInfo identifies the connecting AI agent.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeResult is the server's initialize response.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

// Capabilities declares what the server supports.
type Capabilities struct {
	Tools ToolsCapability `json:"tools"`
}

// ToolsCapability describes tool support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ToolListResult is returned by tools/list.
type ToolListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// ToolCallParams is the payload for tools/call.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResult is the response from tools/call.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is a typed content item in a tool result.
type ContentBlock struct {
	Type string `json:"type"` // "text" or "image"
	Text string `json:"text,omitempty"`
}

// AsyncJob tracks an in-progress background job.
type AsyncJob struct {
	ID        string         `json:"id"`
	Status    string         `json:"status"` // pending, running, completed, failed
	CreatedAt time.Time      `json:"created_at"`
	Results   []any          `json:"results,omitempty"`
	Error     string         `json:"error,omitempty"`
	mu        sync.Mutex
	items     []map[string]any
}

// Server is the MCP server that dispatches JSON-RPC calls to tool handlers.
type Server struct {
	logger    *slog.Logger
	tools     *ToolRegistry
	jobs      map[string]*AsyncJob
	jobsMu    sync.RWMutex
	apiKey    string // required for HTTP transport; empty = no auth (stdio)
	transport Transport
}

// NewServer creates a new MCP server.
func NewServer(logger *slog.Logger, apiKey string) *Server {
	s := &Server{
		logger: logger.With("component", "mcp_server"),
		jobs:   make(map[string]*AsyncJob),
		apiKey: apiKey,
	}
	s.tools = NewToolRegistry(s, logger)
	return s
}

// SetTransport configures the communication transport.
func (s *Server) SetTransport(t Transport) {
	s.transport = t
}

// Run starts the server and blocks until the context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if s.transport == nil {
		return fmt.Errorf("no transport configured")
	}
	s.logger.Info("MCP server starting",
		"protocol", ProtocolVersion,
		"tools", len(s.tools.List()),
	)
	return s.transport.Run(ctx, s.HandleMessage)
}

// HandleMessage processes a single JSON-RPC message and returns a response.
func (s *Server) HandleMessage(ctx context.Context, raw []byte) ([]byte, error) {
	var msg JSONRPCMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return s.errorResponse(nil, ErrCodeParseError, "parse error", err)
	}

	if msg.JSONRPC != "2.0" {
		return s.errorResponse(msg.ID, ErrCodeInvalidRequest, "invalid jsonrpc version", nil)
	}

	// Notifications (no ID) — acknowledge silently.
	if msg.ID == nil || string(msg.ID) == "null" {
		switch msg.Method {
		case "notifications/initialized":
			s.logger.Debug("client initialized")
		case "notifications/cancelled":
			s.logger.Debug("client cancelled request")
		default:
			s.logger.Debug("unknown notification", "method", msg.Method)
		}
		return nil, nil // Notifications have no response.
	}

	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg)
	case "tools/list":
		return s.handleToolsList(msg)
	case "tools/call":
		return s.handleToolsCall(ctx, msg)
	case "ping":
		return s.successResponse(msg.ID, map[string]string{})
	default:
		return s.errorResponse(msg.ID, ErrCodeMethodNotFound,
			fmt.Sprintf("method not found: %s", msg.Method), nil)
	}
}

func (s *Server) handleInitialize(msg JSONRPCMessage) ([]byte, error) {
	var params InitializeParams
	if msg.Params != nil {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return s.errorResponse(msg.ID, ErrCodeInvalidParams, "invalid initialize params", err)
		}
	}
	s.logger.Info("client connected",
		"client", params.ClientInfo.Name,
		"client_version", params.ClientInfo.Version,
	)
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: Capabilities{
			Tools: ToolsCapability{ListChanged: false},
		},
		ServerInfo: ServerInfo{
			Name:    "scrapegoat",
			Version: "1.0.0",
		},
	}
	return s.successResponse(msg.ID, result)
}

func (s *Server) handleToolsList(msg JSONRPCMessage) ([]byte, error) {
	result := ToolListResult{
		Tools: s.tools.List(),
	}
	return s.successResponse(msg.ID, result)
}

func (s *Server) handleToolsCall(ctx context.Context, msg JSONRPCMessage) ([]byte, error) {
	var params ToolCallParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return s.errorResponse(msg.ID, ErrCodeInvalidParams, "invalid tool call params", err)
	}

	s.logger.Info("tool call", "tool", params.Name)

	result, err := s.tools.Execute(ctx, params.Name, params.Arguments)
	if err != nil {
		s.logger.Error("tool execution failed", "tool", params.Name, "error", err)
		toolResult := ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
			IsError: true,
		}
		return s.successResponse(msg.ID, toolResult)
	}

	return s.successResponse(msg.ID, result)
}

// CreateJob creates a new async job and returns its ID.
func (s *Server) CreateJob() *AsyncJob {
	job := &AsyncJob{
		ID:        uuid.New().String(),
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	s.jobsMu.Lock()
	s.jobs[job.ID] = job
	s.jobsMu.Unlock()
	return job
}

// GetJob retrieves a job by ID.
func (s *Server) GetJob(id string) (*AsyncJob, bool) {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	job, ok := s.jobs[id]
	return job, ok
}

// AddJobItem appends a scraped item to a job's results.
func (j *AsyncJob) AddItem(item map[string]any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.items = append(j.items, item)
}

// GetItems returns all collected items.
func (j *AsyncJob) GetItems() []map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]map[string]any, len(j.items))
	copy(result, j.items)
	return result
}

func (s *Server) successResponse(id json.RawMessage, result any) ([]byte, error) {
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	resp := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  resultBytes,
	}
	return json.Marshal(resp)
}

func (s *Server) errorResponse(id json.RawMessage, code int, message string, data any) ([]byte, error) {
	resp := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	return json.Marshal(resp)
}

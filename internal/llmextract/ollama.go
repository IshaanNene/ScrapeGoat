package llmextract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// OllamaExtractor uses a local Ollama instance for zero-cost extraction.
type OllamaExtractor struct {
	model      string
	endpoint   string
	logger     *slog.Logger
	client     *http.Client
	totalUsage TokenUsage
	mu         sync.Mutex
}

// OllamaConfig configures the Ollama extractor.
type OllamaConfig struct {
	Model    string // default: llama3.1
	Endpoint string // default: http://localhost:11434
}

// NewOllamaExtractor creates a local Ollama-backed extractor.
func NewOllamaExtractor(cfg OllamaConfig, logger *slog.Logger) *OllamaExtractor {
	if cfg.Model == "" {
		cfg.Model = "llama3.1"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:11434"
	}
	return &OllamaExtractor{
		model:    cfg.Model,
		endpoint: cfg.Endpoint,
		logger:   logger.With("component", "ollama_extractor"),
		client:   &http.Client{Timeout: 300 * time.Second}, // Local models can be slow.
	}
}

func (e *OllamaExtractor) Name() string { return "ollama" }

// Extract sends HTML to Ollama and returns structured data matching the schema.
func (e *OllamaExtractor) Extract(ctx context.Context, html string, schema ExtractionSchema) (map[string]any, error) {
	// Ollama has smaller context windows — chunk more aggressively.
	chunks := ChunkHTML(html, 8000)

	var results []map[string]any
	var totalUsage TokenUsage

	for i, chunk := range chunks {
		e.logger.Debug("extracting chunk", "chunk", i+1, "total", len(chunks))

		prompt := BuildPrompt(chunk, schema)

		// Build JSON schema for structured output.
		properties := make(map[string]any)
		required := []string{}
		for _, field := range schema.Fields {
			properties[field.Name] = map[string]any{
				"type":        jsonSchemaType(field.Type),
				"description": field.Description,
			}
			if field.Required {
				required = append(required, field.Name)
			}
		}

		reqBody := map[string]any{
			"model":  e.model,
			"prompt": prompt,
			"stream": false,
			"format": map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
			"options": map[string]any{
				"temperature": 0,
				"num_predict": 4096,
			},
		}

		result, usage, err := e.callAPI(ctx, reqBody)
		if err != nil {
			return nil, fmt.Errorf("chunk %d/%d: %w", i+1, len(chunks), err)
		}

		results = append(results, result)
		totalUsage.PromptTokens += usage.PromptTokens
		totalUsage.CompletionTokens += usage.CompletionTokens
		totalUsage.TotalTokens += usage.TotalTokens
	}

	e.mu.Lock()
	e.totalUsage.PromptTokens += totalUsage.PromptTokens
	e.totalUsage.CompletionTokens += totalUsage.CompletionTokens
	e.totalUsage.TotalTokens += totalUsage.TotalTokens
	e.mu.Unlock()

	return MergeResults(results, schema), nil
}

// ExtractBatch processes multiple pages sequentially (Ollama is typically single-GPU).
func (e *OllamaExtractor) ExtractBatch(ctx context.Context, pages []string, schema ExtractionSchema) ([]map[string]any, error) {
	results := make([]map[string]any, 0, len(pages))

	for i, page := range pages {
		e.logger.Info("batch extraction", "page", i+1, "total", len(pages))
		result, err := e.Extract(ctx, page, schema)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i, err)
		}
		results = append(results, result)
	}
	return results, nil
}

// TotalUsage returns cumulative token usage.
func (e *OllamaExtractor) TotalUsage() TokenUsage {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.totalUsage
}

func (e *OllamaExtractor) callAPI(ctx context.Context, reqBody map[string]any) (map[string]any, TokenUsage, error) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.endpoint+"/api/generate", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, TokenUsage{}, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var apiResp ollamaResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, TokenUsage{}, fmt.Errorf("unmarshal response: %w", err)
	}

	usage := TokenUsage{
		PromptTokens:     apiResp.PromptEvalCount,
		CompletionTokens: apiResp.EvalCount,
		TotalTokens:      apiResp.PromptEvalCount + apiResp.EvalCount,
	}

	// Parse the response text as JSON.
	cleaned := cleanJSONResponse(apiResp.Response)
	var result map[string]any
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return nil, usage, fmt.Errorf("unmarshal response JSON: %w (response: %s)", err, truncate(cleaned, 200))
	}

	return result, usage, nil
}

// --- Ollama API Response ---

type ollamaResponse struct {
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

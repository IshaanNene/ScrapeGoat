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

// AnthropicExtractor uses the Anthropic Claude API for structured extraction.
type AnthropicExtractor struct {
	apiKey     string
	model      string
	endpoint   string
	logger     *slog.Logger
	client     *http.Client
	totalUsage TokenUsage
	mu         sync.Mutex
}

// AnthropicConfig configures the Anthropic extractor.
type AnthropicConfig struct {
	APIKey   string
	Model    string // default: claude-3-5-haiku-latest
	Endpoint string // default: https://api.anthropic.com/v1
}

// NewAnthropicExtractor creates a Claude-backed extractor.
func NewAnthropicExtractor(cfg AnthropicConfig, logger *slog.Logger) *AnthropicExtractor {
	if cfg.Model == "" {
		cfg.Model = "claude-3-5-haiku-latest"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.anthropic.com/v1"
	}
	return &AnthropicExtractor{
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		endpoint: cfg.Endpoint,
		logger:   logger.With("component", "anthropic_extractor"),
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

func (e *AnthropicExtractor) Name() string { return "anthropic" }

// Extract sends HTML to Claude and returns structured data matching the schema.
func (e *AnthropicExtractor) Extract(ctx context.Context, html string, schema ExtractionSchema) (map[string]any, error) {
	chunks := ChunkHTML(html, 100000)

	var results []map[string]any
	var totalUsage TokenUsage

	for i, chunk := range chunks {
		e.logger.Debug("extracting chunk", "chunk", i+1, "total", len(chunks))

		prompt := BuildPrompt(chunk, schema)

		// Build tool definition for structured output.
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
			"model":      e.model,
			"max_tokens": 4096,
			"tools": []map[string]any{
				{
					"name":        "extract_data",
					"description": fmt.Sprintf("Extract %s data from the HTML", schema.Name),
					"input_schema": map[string]any{
						"type":       "object",
						"properties": properties,
						"required":   required,
					},
				},
			},
			"tool_choice": map[string]any{
				"type": "tool",
				"name": "extract_data",
			},
			"messages": []map[string]any{
				{"role": "user", "content": prompt},
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

// ExtractBatch processes multiple pages concurrently.
func (e *AnthropicExtractor) ExtractBatch(ctx context.Context, pages []string, schema ExtractionSchema) ([]map[string]any, error) {
	results := make([]map[string]any, len(pages))
	errs := make([]error, len(pages))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 3) // Anthropic has tighter rate limits.

	for i, page := range pages {
		wg.Add(1)
		go func(idx int, html string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := e.Extract(ctx, html, schema)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = result
		}(i, page)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i, err)
		}
	}
	return results, nil
}

// TotalUsage returns cumulative token usage.
func (e *AnthropicExtractor) TotalUsage() TokenUsage {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.totalUsage
}

// TotalCost returns the estimated cumulative cost in USD.
func (e *AnthropicExtractor) TotalCost() float64 {
	return EstimateCost(e.model, e.TotalUsage())
}

func (e *AnthropicExtractor) callAPI(ctx context.Context, reqBody map[string]any) (map[string]any, TokenUsage, error) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.endpoint+"/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", e.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, TokenUsage{}, fmt.Errorf("unmarshal response: %w", err)
	}

	usage := TokenUsage{
		PromptTokens:     apiResp.Usage.InputTokens,
		CompletionTokens: apiResp.Usage.OutputTokens,
		TotalTokens:      apiResp.Usage.InputTokens + apiResp.Usage.OutputTokens,
	}

	// Look for tool_use content block.
	for _, block := range apiResp.Content {
		if block.Type == "tool_use" {
			var result map[string]any
			inputBytes, err := json.Marshal(block.Input)
			if err != nil {
				return nil, usage, fmt.Errorf("marshal tool input: %w", err)
			}
			if err := json.Unmarshal(inputBytes, &result); err != nil {
				return nil, usage, fmt.Errorf("unmarshal tool input: %w", err)
			}
			return result, usage, nil
		}
	}

	// Fallback: parse text content as JSON.
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			cleaned := cleanJSONResponse(block.Text)
			var result map[string]any
			if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
				continue
			}
			return result, usage, nil
		}
	}

	return nil, usage, fmt.Errorf("no extractable content in response")
}

// --- Anthropic API Response Types ---

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicContentBlock struct {
	Type  string `json:"type"` // "text" or "tool_use"
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

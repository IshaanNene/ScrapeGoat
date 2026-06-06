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

// OpenAIExtractor uses the OpenAI API for structured data extraction.
type OpenAIExtractor struct {
	apiKey     string
	model      string
	endpoint   string
	logger     *slog.Logger
	client     *http.Client
	totalUsage TokenUsage
	mu         sync.Mutex
}

// OpenAIConfig configures the OpenAI extractor.
type OpenAIConfig struct {
	APIKey   string
	Model    string // default: gpt-4o
	Endpoint string // default: https://api.openai.com/v1
}

// NewOpenAIExtractor creates an OpenAI-backed extractor.
func NewOpenAIExtractor(cfg OpenAIConfig, logger *slog.Logger) *OpenAIExtractor {
	if cfg.Model == "" {
		cfg.Model = "gpt-4o"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.openai.com/v1"
	}
	return &OpenAIExtractor{
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		endpoint: cfg.Endpoint,
		logger:   logger.With("component", "openai_extractor"),
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

func (e *OpenAIExtractor) Name() string { return "openai" }

// Extract sends HTML to OpenAI and returns structured data matching the schema.
func (e *OpenAIExtractor) Extract(ctx context.Context, html string, schema ExtractionSchema) (map[string]any, error) {
	chunks := ChunkHTML(html, 100000) // ~100k tokens per chunk

	var results []map[string]any
	var totalUsage TokenUsage

	for i, chunk := range chunks {
		e.logger.Debug("extracting chunk", "chunk", i+1, "total", len(chunks), "chars", len(chunk))

		prompt := BuildPrompt(chunk, schema)

		// Build the function schema for structured output.
		properties := make(map[string]any)
		required := []string{}
		for _, field := range schema.Fields {
			prop := map[string]any{
				"type":        jsonSchemaType(field.Type),
				"description": field.Description,
			}
			properties[field.Name] = prop
			if field.Required {
				required = append(required, field.Name)
			}
		}

		reqBody := map[string]any{
			"model": e.model,
			"messages": []map[string]any{
				{"role": "system", "content": "You are a data extraction assistant. Extract structured data from HTML and return valid JSON only."},
				{"role": "user", "content": prompt},
			},
			"tools": []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name":        "extract_data",
						"description": fmt.Sprintf("Extract %s data from the HTML", schema.Name),
						"parameters": map[string]any{
							"type":       "object",
							"properties": properties,
							"required":   required,
						},
					},
				},
			},
			"tool_choice": map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "extract_data",
				},
			},
			"temperature": 0,
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
func (e *OpenAIExtractor) ExtractBatch(ctx context.Context, pages []string, schema ExtractionSchema) ([]map[string]any, error) {
	results := make([]map[string]any, len(pages))
	errs := make([]error, len(pages))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // Max 5 concurrent API calls.

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

	// Check for errors.
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i, err)
		}
	}
	return results, nil
}

// TotalUsage returns the cumulative token usage across all calls.
func (e *OpenAIExtractor) TotalUsage() TokenUsage {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.totalUsage
}

// TotalCost returns the estimated cumulative cost in USD.
func (e *OpenAIExtractor) TotalCost() float64 {
	return EstimateCost(e.model, e.TotalUsage())
}

func (e *OpenAIExtractor) callAPI(ctx context.Context, reqBody map[string]any) (map[string]any, TokenUsage, error) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.endpoint+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, TokenUsage{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

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

	var apiResp openAIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, TokenUsage{}, fmt.Errorf("unmarshal response: %w", err)
	}

	usage := TokenUsage{
		PromptTokens:     apiResp.Usage.PromptTokens,
		CompletionTokens: apiResp.Usage.CompletionTokens,
		TotalTokens:      apiResp.Usage.TotalTokens,
	}

	// Extract the function call arguments.
	if len(apiResp.Choices) == 0 {
		return nil, usage, fmt.Errorf("no choices in response")
	}

	choice := apiResp.Choices[0]

	// Check for tool calls (function calling).
	if len(choice.Message.ToolCalls) > 0 {
		argsJSON := choice.Message.ToolCalls[0].Function.Arguments
		var result map[string]any
		if err := json.Unmarshal([]byte(argsJSON), &result); err != nil {
			return nil, usage, fmt.Errorf("unmarshal tool call args: %w", err)
		}
		return result, usage, nil
	}

	// Fallback: parse content as JSON.
	content := choice.Message.Content
	content = cleanJSONResponse(content)

	var result map[string]any
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, usage, fmt.Errorf("unmarshal content as JSON: %w (content: %s)", err, truncate(content, 200))
	}
	return result, usage, nil
}

// --- OpenAI API Response Types ---

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIChoice struct {
	Message struct {
		Content   string           `json:"content"`
		ToolCalls []openAIToolCall `json:"tool_calls"`
	} `json:"message"`
}

type openAIToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// --- Helpers ---

func jsonSchemaType(t string) string {
	switch t {
	case "string", "number", "boolean", "array", "object":
		return t
	case "int", "integer":
		return "integer"
	case "float", "double":
		return "number"
	default:
		return "string"
	}
}

func cleanJSONResponse(s string) string {
	// Strip markdown code fences if present.
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

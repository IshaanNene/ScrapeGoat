// The LLM client and pipeline processors below used to live in internal/ai. They
// were imported by this example and by nothing else, which made them part of the
// module's internal surface — and therefore something to keep compiling and
// reviewing — for the benefit of one example. Folding them in here removes that
// obligation without deleting working code.
//
// They are also a deliberate statement about where AI belongs: as a processor
// something opts into, in an example, not as a stage in the crawl path. Deleting
// every model provider must leave the architecture unchanged and still useful.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// LLMProvider specifies which LLM backend to use.
type LLMProvider string

const (
	ProviderOllama LLMProvider = "ollama"
	ProviderOpenAI LLMProvider = "openai"
	ProviderCustom LLMProvider = "custom"
)

// LLMConfig configures the LLM integration.
type LLMConfig struct {
	Provider    LLMProvider
	Endpoint    string // e.g. "http://localhost:11434" for Ollama
	Model       string // e.g. "llama3", "gpt-4o-mini"
	APIKey      string
	MaxTokens   int
	Temperature float64
}

// LLMClient communicates with an LLM for AI-assisted processing.
type LLMClient struct {
	cfg    LLMConfig
	client *http.Client
	logger *slog.Logger
}

// NewLLMClient creates a new LLM client.
func NewLLMClient(cfg LLMConfig, logger *slog.Logger) *LLMClient {
	return &LLMClient{
		cfg: cfg,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		logger: logger.With("component", "llm_client"),
	}
}

// Generate sends a prompt to the LLM and returns the response.
func (c *LLMClient) Generate(ctx context.Context, prompt string) (string, error) {
	switch c.cfg.Provider {
	case ProviderOllama:
		return c.generateOllama(ctx, prompt)
	case ProviderOpenAI:
		return c.generateOpenAI(ctx, prompt)
	case ProviderCustom:
		return c.generateCustom(ctx, prompt)
	default:
		return "", fmt.Errorf("unsupported LLM provider: %s", c.cfg.Provider)
	}
}

func (c *LLMClient) generateOllama(ctx context.Context, prompt string) (string, error) {
	payload := map[string]any{
		"model":  c.cfg.Model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"temperature": c.cfg.Temperature,
			"num_predict": c.cfg.MaxTokens,
		},
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.Endpoint+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode ollama response: %w", err)
	}
	return result.Response, nil
}

func (c *LLMClient) generateOpenAI(ctx context.Context, prompt string) (string, error) {
	payload := map[string]any{
		"model": c.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  c.cfg.MaxTokens,
		"temperature": c.cfg.Temperature,
	}

	body, _ := json.Marshal(payload)
	endpoint := c.cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in openai response")
	}
	return result.Choices[0].Message.Content, nil
}

func (c *LLMClient) generateCustom(ctx context.Context, prompt string) (string, error) {
	payload := map[string]any{
		"prompt": prompt,
		"model":  c.cfg.Model,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(respBody), nil
}

// --- AI Processing Pipeline Middleware ---

// Summarizer summarizes scraped content using an LLM.
type Summarizer struct {
	client    *LLMClient
	fields    []string
	maxLength int
	logger    *slog.Logger
}

// NewSummarizer creates a new content summarizer.
func NewSummarizer(client *LLMClient, fields []string, maxLength int, logger *slog.Logger) *Summarizer {
	return &Summarizer{client: client, fields: fields, maxLength: maxLength, logger: logger}
}

func (s *Summarizer) Name() string { return "ai_summarizer" }

func (s *Summarizer) Process(item *types.Item) (*types.Item, error) {
	for _, field := range s.fields {
		text := item.GetString(field)
		if text == "" || len(text) < 100 {
			continue
		}
		if len(text) > 4000 {
			text = text[:4000]
		}

		prompt := fmt.Sprintf("Summarize the following text in 2-3 sentences:\n\n%s", text)
		summary, err := s.client.Generate(context.Background(), prompt)
		if err != nil {
			s.logger.Warn("summarization failed", "field", field, "error", err)
			continue
		}
		item.Set(field+"_summary", strings.TrimSpace(summary))
	}
	return item, nil
}

// NERExtractor extracts named entities using an LLM.
type NERExtractor struct {
	client *LLMClient
	fields []string
	logger *slog.Logger
}

// NewNERExtractor creates a new named entity recognition extractor.
func NewNERExtractor(client *LLMClient, fields []string, logger *slog.Logger) *NERExtractor {
	return &NERExtractor{client: client, fields: fields, logger: logger}
}

func (n *NERExtractor) Name() string { return "ai_ner" }

func (n *NERExtractor) Process(item *types.Item) (*types.Item, error) {
	for _, field := range n.fields {
		text := item.GetString(field)
		if text == "" {
			continue
		}
		if len(text) > 3000 {
			text = text[:3000]
		}

		prompt := fmt.Sprintf(`Extract named entities from the following text. Return JSON with keys: "persons", "organizations", "locations", "dates", "monetary_values". Each should be an array of strings.

Text: %s`, text)

		response, err := n.client.Generate(context.Background(), prompt)
		if err != nil {
			n.logger.Warn("NER extraction failed", "field", field, "error", err)
			continue
		}

		var entities map[string][]string
		if err := json.Unmarshal([]byte(extractJSON(response)), &entities); err == nil {
			item.Set(field+"_entities", entities)
		}
	}
	return item, nil
}

// SentimentAnalyzer tags content with sentiment scores.
type SentimentAnalyzer struct {
	client *LLMClient
	fields []string
	logger *slog.Logger
}

// NewSentimentAnalyzer creates a new sentiment analyzer.
func NewSentimentAnalyzer(client *LLMClient, fields []string, logger *slog.Logger) *SentimentAnalyzer {
	return &SentimentAnalyzer{client: client, fields: fields, logger: logger}
}

func (s *SentimentAnalyzer) Name() string { return "ai_sentiment" }

func (s *SentimentAnalyzer) Process(item *types.Item) (*types.Item, error) {
	for _, field := range s.fields {
		text := item.GetString(field)
		if text == "" {
			continue
		}
		if len(text) > 2000 {
			text = text[:2000]
		}

		prompt := fmt.Sprintf(`Analyze the sentiment of the following text. Return JSON with:
- "sentiment": "positive", "negative", "neutral", or "mixed"
- "score": float from -1.0 (very negative) to 1.0 (very positive)
- "keywords": array of key sentiment-bearing words

Text: %s`, text)

		response, err := s.client.Generate(context.Background(), prompt)
		if err != nil {
			s.logger.Warn("sentiment analysis failed", "field", field, "error", err)
			continue
		}

		var sentiment map[string]any
		if err := json.Unmarshal([]byte(extractJSON(response)), &sentiment); err == nil {
			item.Set(field+"_sentiment", sentiment)
		}
	}
	return item, nil
}

// ContentFilter pre-filters content before sending to LLM.
type ContentFilter struct {
	client      *LLMClient
	fields      []string
	criteria    string
	dropIfFails bool
	logger      *slog.Logger
}

// NewContentFilter creates a filter that uses LLM to evaluate content relevance.
func NewContentFilter(client *LLMClient, fields []string, criteria string, dropIfFails bool, logger *slog.Logger) *ContentFilter {
	return &ContentFilter{
		client: client, fields: fields, criteria: criteria,
		dropIfFails: dropIfFails, logger: logger,
	}
}

func (f *ContentFilter) Name() string { return "ai_content_filter" }

func (f *ContentFilter) Process(item *types.Item) (*types.Item, error) {
	for _, field := range f.fields {
		text := item.GetString(field)
		if text == "" {
			continue
		}
		if len(text) > 2000 {
			text = text[:2000]
		}

		prompt := fmt.Sprintf(`Does the following text match this criteria: "%s"? 
Answer with just "yes" or "no".

Text: %s`, f.criteria, text)

		response, err := f.client.Generate(context.Background(), prompt)
		if err != nil {
			f.logger.Warn("content filter failed", "field", field, "error", err)
			continue
		}

		matches := strings.Contains(strings.ToLower(response), "yes")
		item.Set(field+"_relevant", matches)

		if f.dropIfFails && !matches {
			return nil, nil
		}
	}
	return item, nil
}

// extractJSON tries to find a JSON object in the LLM response.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return "{}"
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return "{}"
}

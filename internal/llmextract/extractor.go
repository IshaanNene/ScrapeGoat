// Package llmextract provides a schema-driven LLM extraction pipeline that
// uses large language models to extract structured data from HTML content.
// It supports OpenAI, Anthropic, and Ollama backends with caching, smart
// chunking, and cost tracking.
package llmextract

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ExtractionSchema defines the shape of data to extract from HTML.
type ExtractionSchema struct {
	// Name identifies this schema.
	Name string `json:"name" yaml:"name"`

	// Description explains what data this schema extracts.
	Description string `json:"description" yaml:"description"`

	// Fields lists the individual data fields to extract.
	Fields []FieldDef `json:"fields" yaml:"fields"`
}

// FieldDef defines a single field within an extraction schema.
type FieldDef struct {
	// Name is the output key for this field.
	Name string `json:"name" yaml:"name"`

	// Type is the expected value type: string, number, boolean, array, object.
	Type string `json:"type" yaml:"type"`

	// Description tells the LLM what this field should contain.
	Description string `json:"description" yaml:"description"`

	// Required indicates whether extraction should fail if this field is missing.
	Required bool `json:"required" yaml:"required"`

	// Example provides an example value to guide the LLM.
	Example any `json:"example,omitempty" yaml:"example,omitempty"`
}

// LLMExtractor is the interface for all LLM extraction backends.
type LLMExtractor interface {
	// Extract extracts structured data from HTML using the given schema.
	Extract(ctx context.Context, html string, schema ExtractionSchema) (map[string]any, error)

	// ExtractBatch extracts data from multiple HTML pages concurrently.
	ExtractBatch(ctx context.Context, pages []string, schema ExtractionSchema) ([]map[string]any, error)

	// Name returns the backend identifier (e.g., "openai", "anthropic", "ollama").
	Name() string
}

// ExtractionResult holds the output of an extraction along with metadata.
type ExtractionResult struct {
	Data       map[string]any `json:"data"`
	TokensUsed TokenUsage     `json:"tokens_used"`
	CostUSD    float64        `json:"cost_usd"`
	Cached     bool           `json:"cached"`
	Chunks     int            `json:"chunks"`
}

// TokenUsage tracks LLM token consumption.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// --- Smart HTML Chunking ---

// ChunkHTML splits large HTML content into semantic sections for processing.
// If the HTML is smaller than maxTokens, it returns a single chunk.
// Otherwise it splits on semantic boundaries (headings, sections, articles).
func ChunkHTML(html string, maxTokens int) []string {
	// Rough token estimate: ~4 chars per token for English.
	maxChars := maxTokens * 4
	if len(html) <= maxChars {
		return []string{html}
	}

	// Split on semantic HTML boundaries.
	sectionRe := regexp.MustCompile(`(?i)<(?:h[1-6]|section|article|main|header|footer|nav|div\s+class)[^>]*>`)
	indices := sectionRe.FindAllStringIndex(html, -1)

	if len(indices) == 0 {
		// No semantic boundaries — split by character count.
		return splitBySize(html, maxChars)
	}

	var chunks []string
	start := 0

	for _, idx := range indices {
		if idx[0]-start >= maxChars && start < idx[0] {
			chunks = append(chunks, html[start:idx[0]])
			start = idx[0]
		}
	}
	// Add remaining content.
	if start < len(html) {
		chunks = append(chunks, html[start:])
	}

	// Further split any chunks that are still too large.
	var result []string
	for _, chunk := range chunks {
		if len(chunk) > maxChars {
			result = append(result, splitBySize(chunk, maxChars)...)
		} else if len(strings.TrimSpace(chunk)) > 0 {
			result = append(result, chunk)
		}
	}

	return result
}

// splitBySize splits text into chunks of approximately maxChars.
func splitBySize(text string, maxChars int) []string {
	var chunks []string
	for len(text) > maxChars {
		// Try to break at a paragraph or newline boundary.
		splitPoint := maxChars
		if idx := strings.LastIndex(text[:maxChars], "\n"); idx > maxChars/2 {
			splitPoint = idx + 1
		}
		chunks = append(chunks, text[:splitPoint])
		text = text[splitPoint:]
	}
	if len(strings.TrimSpace(text)) > 0 {
		chunks = append(chunks, text)
	}
	return chunks
}

// MergeResults combines extraction results from multiple chunks.
// For string fields, it concatenates. For arrays, it merges. For others, last wins.
func MergeResults(results []map[string]any, schema ExtractionSchema) map[string]any {
	if len(results) == 0 {
		return make(map[string]any)
	}
	if len(results) == 1 {
		return results[0]
	}

	fieldTypes := make(map[string]string)
	for _, f := range schema.Fields {
		fieldTypes[f.Name] = f.Type
	}

	merged := make(map[string]any)
	for _, result := range results {
		for key, val := range result {
			existing, exists := merged[key]
			if !exists {
				merged[key] = val
				continue
			}

			switch fieldTypes[key] {
			case "array":
				// Merge arrays.
				existArr, ok1 := existing.([]any)
				newArr, ok2 := val.([]any)
				if ok1 && ok2 {
					merged[key] = append(existArr, newArr...)
				} else {
					merged[key] = val
				}
			case "string":
				// Concatenate strings with separator.
				existStr, ok1 := existing.(string)
				newStr, ok2 := val.(string)
				if ok1 && ok2 && newStr != "" {
					if existStr != "" {
						merged[key] = existStr + " " + newStr
					} else {
						merged[key] = newStr
					}
				}
			default:
				// Last non-nil value wins.
				if val != nil {
					merged[key] = val
				}
			}
		}
	}

	return merged
}

// CacheKey generates a deterministic cache key from HTML content and schema.
// The key is SHA256(html_content + "|" + schema_json) to ensure schema
// changes invalidate the cache.
func CacheKey(html string, schema ExtractionSchema) string {
	schemaJSON, _ := json.Marshal(schema)
	combined := html + "|" + string(schemaJSON)
	hash := sha256.Sum256([]byte(combined))
	return fmt.Sprintf("%x", hash)
}

// BuildPrompt constructs the LLM prompt for structured extraction.
func BuildPrompt(html string, schema ExtractionSchema) string {
	var sb strings.Builder

	sb.WriteString("Extract the following structured data from the HTML content below.\n\n")
	sb.WriteString("## Schema\n\n")

	for _, field := range schema.Fields {
		required := ""
		if field.Required {
			required = " (REQUIRED)"
		}
		sb.WriteString(fmt.Sprintf("- **%s** (%s)%s: %s", field.Name, field.Type, required, field.Description))
		if field.Example != nil {
			sb.WriteString(fmt.Sprintf(" Example: %v", field.Example))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n## Instructions\n\n")
	sb.WriteString("1. Return ONLY valid JSON with the extracted fields.\n")
	sb.WriteString("2. Use null for fields that cannot be found.\n")
	sb.WriteString("3. Follow the specified types exactly.\n")
	sb.WriteString("4. Do not include any explanation or markdown formatting.\n\n")

	sb.WriteString("## HTML Content\n\n```html\n")
	// Truncate very long HTML to avoid exceeding context.
	if len(html) > 100000 {
		sb.WriteString(html[:100000])
		sb.WriteString("\n... [truncated]")
	} else {
		sb.WriteString(html)
	}
	sb.WriteString("\n```\n")

	return sb.String()
}

// EstimateTokens provides a rough token count for a string (~4 chars per token).
func EstimateTokens(text string) int {
	return len(text) / 4
}

// EstimateCost calculates approximate USD cost based on model and token usage.
func EstimateCost(model string, usage TokenUsage) float64 {
	// Pricing per 1M tokens (input/output) as of 2025.
	pricing := map[string][2]float64{
		"gpt-4o":            {2.50, 10.00},
		"gpt-4o-mini":       {0.15, 0.60},
		"gpt-4-turbo":       {10.00, 30.00},
		"claude-3-5-haiku":  {0.25, 1.25},
		"claude-3-5-sonnet": {3.00, 15.00},
		"claude-3-opus":     {15.00, 75.00},
	}

	rate, ok := pricing[model]
	if !ok {
		return 0 // Unknown model or local (Ollama) — free.
	}

	inputCost := float64(usage.PromptTokens) / 1_000_000 * rate[0]
	outputCost := float64(usage.CompletionTokens) / 1_000_000 * rate[1]
	return inputCost + outputCost
}

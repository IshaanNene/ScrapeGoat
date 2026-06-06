package llmextract

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testSchema() ExtractionSchema {
	return ExtractionSchema{
		Name:        "product",
		Description: "Product information",
		Fields: []FieldDef{
			{Name: "title", Type: "string", Description: "Product title", Required: true},
			{Name: "price", Type: "number", Description: "Price in USD", Required: true},
			{Name: "description", Type: "string", Description: "Product description"},
			{Name: "tags", Type: "array", Description: "Product tags"},
			{Name: "in_stock", Type: "boolean", Description: "Whether the product is in stock"},
		},
	}
}

// --- Chunking Tests ---

func TestChunkHTML_SmallDocument(t *testing.T) {
	html := "<html><body><h1>Hello</h1><p>World</p></body></html>"
	chunks := ChunkHTML(html, 100000)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for small doc, got %d", len(chunks))
	}
}

func TestChunkHTML_LargeDocument(t *testing.T) {
	// Create a large HTML document with multiple sections.
	var sb strings.Builder
	sb.WriteString("<html><body>")
	for i := 0; i < 50; i++ {
		sb.WriteString("<section>")
		sb.WriteString("<h2>Section ")
		sb.WriteString(strings.Repeat("x", 1000))
		sb.WriteString("</h2>")
		sb.WriteString("<p>")
		sb.WriteString(strings.Repeat("content ", 500))
		sb.WriteString("</p>")
		sb.WriteString("</section>")
	}
	sb.WriteString("</body></html>")

	html := sb.String()
	chunks := ChunkHTML(html, 2000) // ~8000 chars per chunk

	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks for large doc, got %d", len(chunks))
	}

	// All chunks should be non-empty.
	for i, chunk := range chunks {
		if len(strings.TrimSpace(chunk)) == 0 {
			t.Errorf("chunk %d is empty", i)
		}
	}
}

func TestChunkHTML_NoSemanticBoundaries(t *testing.T) {
	html := strings.Repeat("a", 50000) // 50k chars, no HTML structure
	chunks := ChunkHTML(html, 5000)     // ~20k chars max

	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(chunks))
	}
}

// --- Merge Tests ---

func TestMergeResults_SingleResult(t *testing.T) {
	results := []map[string]any{
		{"title": "Product A", "price": 29.99},
	}
	merged := MergeResults(results, testSchema())

	if merged["title"] != "Product A" {
		t.Errorf("title = %v, want Product A", merged["title"])
	}
	if merged["price"] != 29.99 {
		t.Errorf("price = %v, want 29.99", merged["price"])
	}
}

func TestMergeResults_StringConcatenation(t *testing.T) {
	results := []map[string]any{
		{"title": "Part 1", "description": "First part"},
		{"title": "Part 2", "description": "Second part"},
	}
	merged := MergeResults(results, testSchema())

	desc, ok := merged["description"].(string)
	if !ok || !strings.Contains(desc, "First part") || !strings.Contains(desc, "Second part") {
		t.Errorf("description should contain both parts, got: %v", merged["description"])
	}
}

func TestMergeResults_ArrayMerge(t *testing.T) {
	results := []map[string]any{
		{"tags": []any{"tag1", "tag2"}},
		{"tags": []any{"tag3"}},
	}
	merged := MergeResults(results, testSchema())

	tags, ok := merged["tags"].([]any)
	if !ok || len(tags) != 3 {
		t.Errorf("tags should have 3 elements, got: %v", merged["tags"])
	}
}

func TestMergeResults_Empty(t *testing.T) {
	merged := MergeResults(nil, testSchema())
	if len(merged) != 0 {
		t.Errorf("expected empty map, got %v", merged)
	}
}

// --- Cache Key Tests ---

func TestCacheKey_Deterministic(t *testing.T) {
	html := "<html>test</html>"
	schema := testSchema()

	key1 := CacheKey(html, schema)
	key2 := CacheKey(html, schema)

	if key1 != key2 {
		t.Errorf("cache keys should be deterministic: %s != %s", key1, key2)
	}
}

func TestCacheKey_SchemaChange(t *testing.T) {
	html := "<html>test</html>"
	schema1 := testSchema()
	schema2 := testSchema()
	schema2.Fields = append(schema2.Fields, FieldDef{Name: "new_field", Type: "string"})

	key1 := CacheKey(html, schema1)
	key2 := CacheKey(html, schema2)

	if key1 == key2 {
		t.Error("cache keys should differ when schema changes")
	}
}

func TestCacheKey_HTMLChange(t *testing.T) {
	schema := testSchema()
	key1 := CacheKey("<html>v1</html>", schema)
	key2 := CacheKey("<html>v2</html>", schema)

	if key1 == key2 {
		t.Error("cache keys should differ when HTML changes")
	}
}

// --- Prompt Tests ---

func TestBuildPrompt(t *testing.T) {
	html := "<html><body><h1>Test Product</h1><span class='price'>$29.99</span></body></html>"
	schema := testSchema()

	prompt := BuildPrompt(html, schema)

	if !strings.Contains(prompt, "title") {
		t.Error("prompt should contain field name 'title'")
	}
	if !strings.Contains(prompt, "REQUIRED") {
		t.Error("prompt should mark required fields")
	}
	if !strings.Contains(prompt, "Test Product") {
		t.Error("prompt should contain the HTML content")
	}
	if !strings.Contains(prompt, "valid JSON") {
		t.Error("prompt should instruct to return JSON")
	}
}

// --- Cost Estimation Tests ---

func TestEstimateCost(t *testing.T) {
	tests := []struct {
		model    string
		usage    TokenUsage
		wantMin  float64
		wantMax  float64
	}{
		{
			model:   "gpt-4o",
			usage:   TokenUsage{PromptTokens: 1000, CompletionTokens: 500},
			wantMin: 0.005,
			wantMax: 0.01,
		},
		{
			model:   "claude-3-5-haiku",
			usage:   TokenUsage{PromptTokens: 1000, CompletionTokens: 500},
			wantMin: 0.0001,
			wantMax: 0.002,
		},
		{
			model:   "llama3.1",
			usage:   TokenUsage{PromptTokens: 1000, CompletionTokens: 500},
			wantMin: 0,
			wantMax: 0, // Local model = free.
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			cost := EstimateCost(tt.model, tt.usage)
			if cost < tt.wantMin || cost > tt.wantMax {
				t.Errorf("cost = %f, want [%f, %f]", cost, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	text := strings.Repeat("word ", 1000) // ~5000 chars
	tokens := EstimateTokens(text)
	if tokens < 1000 || tokens > 2000 {
		t.Errorf("token estimate = %d, expected ~1250", tokens)
	}
}

// --- Cache Database Tests ---

func TestCache_PutAndGet(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_cache.db")
	cache, err := NewCache(dbPath, testLogger())
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()
	key := "test-key-abc123"
	entry := &CacheEntry{
		Result:    map[string]any{"title": "Test", "price": 42.0},
		Model:     "test-model",
		Tokens:    100,
		CostUSD:   0.001,
		CreatedAt: time.Now(),
	}

	// Put.
	if err := cache.Put(ctx, key, entry, 1*time.Hour); err != nil {
		t.Fatalf("cache put: %v", err)
	}

	// Get.
	retrieved, err := cache.Get(ctx, key)
	if err != nil {
		t.Fatalf("cache get: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected cache hit, got nil")
	}
	if retrieved.Result["title"] != "Test" {
		t.Errorf("cached title = %v, want Test", retrieved.Result["title"])
	}
}

func TestCache_Miss(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewCache(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	defer cache.Close()

	entry, err := cache.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("cache get: %v", err)
	}
	if entry != nil {
		t.Error("expected nil for cache miss")
	}
}

func TestCache_Stats(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewCache(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()

	// Insert some entries.
	for i := 0; i < 3; i++ {
		entry := &CacheEntry{
			Result:  map[string]any{"i": i},
			Model:   "test",
			Tokens:  100,
			CostUSD: 0.01,
		}
		if err := cache.Put(ctx, strings.Repeat("x", 10+i), entry, 1*time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := cache.Stats(ctx)
	if err != nil {
		t.Fatalf("cache stats: %v", err)
	}
	if stats.EntryCount != 3 {
		t.Errorf("entry count = %d, want 3", stats.EntryCount)
	}
}

func TestCache_Clear(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewCache(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()
	entry := &CacheEntry{Result: map[string]any{"a": 1}, Model: "test"}
	_ = cache.Put(ctx, "key1", entry, 0)

	if err := cache.Clear(ctx); err != nil {
		t.Fatalf("cache clear: %v", err)
	}

	stats, _ := cache.Stats(ctx)
	if stats.EntryCount != 0 {
		t.Errorf("entry count after clear = %d, want 0", stats.EntryCount)
	}
}

// --- CachedExtractor Tests ---

type mockExtractor struct {
	callCount int
	result    map[string]any
}

func (m *mockExtractor) Name() string { return "mock" }

func (m *mockExtractor) Extract(ctx context.Context, html string, schema ExtractionSchema) (map[string]any, error) {
	m.callCount++
	return m.result, nil
}

func (m *mockExtractor) ExtractBatch(ctx context.Context, pages []string, schema ExtractionSchema) ([]map[string]any, error) {
	results := make([]map[string]any, len(pages))
	for i := range pages {
		results[i] = m.result
		m.callCount++
	}
	return results, nil
}

func TestCachedExtractor(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewCache(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	defer cache.Close()

	mock := &mockExtractor{
		result: map[string]any{"title": "Cached Product", "price": 19.99},
	}

	cached := NewCachedExtractor(mock, cache, 1*time.Hour, testLogger())
	ctx := context.Background()
	schema := testSchema()
	html := "<html>test</html>"

	// First call — cache miss.
	result1, err := cached.Extract(ctx, html, schema)
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	if result1["title"] != "Cached Product" {
		t.Errorf("title = %v, want Cached Product", result1["title"])
	}
	if mock.callCount != 1 {
		t.Errorf("mock call count = %d, want 1", mock.callCount)
	}

	// Second call with same HTML+schema — cache hit, mock should NOT be called again.
	result2, err := cached.Extract(ctx, html, schema)
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if result2["title"] != "Cached Product" {
		t.Errorf("title = %v, want Cached Product", result2["title"])
	}
	if mock.callCount != 1 {
		t.Errorf("mock call count = %d, want 1 (should be cached)", mock.callCount)
	}

	// Third call with different HTML — cache miss.
	_, err = cached.Extract(ctx, "<html>different</html>", schema)
	if err != nil {
		t.Fatalf("third extract: %v", err)
	}
	if mock.callCount != 2 {
		t.Errorf("mock call count = %d, want 2", mock.callCount)
	}
}

// --- Interface Compliance ---

var (
	_ LLMExtractor = (*OpenAIExtractor)(nil)
	_ LLMExtractor = (*AnthropicExtractor)(nil)
	_ LLMExtractor = (*OllamaExtractor)(nil)
	_ LLMExtractor = (*CachedExtractor)(nil)
)

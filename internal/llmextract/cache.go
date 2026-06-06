package llmextract

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Cache stores LLM extraction results keyed by SHA256(html+schema).
// This avoids redundant (and expensive) LLM calls on re-crawls.
type Cache struct {
	db     *sql.DB
	logger *slog.Logger
	mu     sync.RWMutex
}

// NewCache opens or creates a SQLite cache at the given path.
func NewCache(dbPath string, logger *slog.Logger) (*Cache, error) {
	// Ensure the directory exists.
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open cache db: %w", err)
	}

	// Create tables.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS extraction_cache (
			cache_key  TEXT PRIMARY KEY,
			result     TEXT NOT NULL,
			model      TEXT NOT NULL,
			tokens     INTEGER NOT NULL DEFAULT 0,
			cost_usd   REAL NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			expires_at TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_cache_created ON extraction_cache(created_at);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create cache tables: %w", err)
	}

	return &Cache{
		db:     db,
		logger: logger.With("component", "llm_cache"),
	}, nil
}

// CacheEntry holds a cached extraction result.
type CacheEntry struct {
	Result    map[string]any `json:"result"`
	Model     string         `json:"model"`
	Tokens    int            `json:"tokens"`
	CostUSD   float64        `json:"cost_usd"`
	CreatedAt time.Time      `json:"created_at"`
}

// Get retrieves a cached result by key. Returns nil if not found or expired.
func (c *Cache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	row := c.db.QueryRowContext(ctx,
		`SELECT result, model, tokens, cost_usd, created_at, expires_at
		 FROM extraction_cache WHERE cache_key = ?`, key)

	var resultJSON, model, createdStr string
	var tokens int
	var costUSD float64
	var expiresStr sql.NullString

	if err := row.Scan(&resultJSON, &model, &tokens, &costUSD, &createdStr, &expiresStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Cache miss.
		}
		return nil, fmt.Errorf("scan cache row: %w", err)
	}

	// Check expiry.
	if expiresStr.Valid {
		expiresAt, err := time.Parse(time.RFC3339, expiresStr.String)
		if err == nil && time.Now().After(expiresAt) {
			c.logger.Debug("cache entry expired", "key", truncateKey(key, 16))
			return nil, nil
		}
	}

	createdAt, _ := time.Parse(time.RFC3339, createdStr)

	var result map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("unmarshal cached result: %w", err)
	}

	return &CacheEntry{
		Result:    result,
		Model:     model,
		Tokens:    tokens,
		CostUSD:   costUSD,
		CreatedAt: createdAt,
	}, nil
}

// Put stores an extraction result in the cache.
func (c *Cache) Put(ctx context.Context, key string, entry *CacheEntry, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	resultJSON, err := json.Marshal(entry.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	var expiresAt sql.NullString
	if ttl > 0 {
		expiresAt = sql.NullString{
			String: time.Now().Add(ttl).Format(time.RFC3339),
			Valid:  true,
		}
	}

	_, err = c.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO extraction_cache
		 (cache_key, result, model, tokens, cost_usd, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		key, string(resultJSON), entry.Model, entry.Tokens, entry.CostUSD,
		time.Now().Format(time.RFC3339), expiresAt)
	if err != nil {
		return fmt.Errorf("insert cache entry: %w", err)
	}

	c.logger.Debug("cached extraction result", "key", truncateKey(key, 16), "model", entry.Model)
	return nil
}

// Delete removes a cache entry.
func (c *Cache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.db.ExecContext(ctx, `DELETE FROM extraction_cache WHERE cache_key = ?`, key)
	return err
}

// Clear removes all cache entries.
func (c *Cache) Clear(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.db.ExecContext(ctx, `DELETE FROM extraction_cache`)
	return err
}

// Stats returns cache statistics.
func (c *Cache) Stats(ctx context.Context) (CacheStats, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var stats CacheStats
	row := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(tokens), 0), COALESCE(SUM(cost_usd), 0)
		 FROM extraction_cache`)
	if err := row.Scan(&stats.EntryCount, &stats.TotalTokens, &stats.TotalCostUSD); err != nil {
		return stats, err
	}

	// Count expired.
	row = c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM extraction_cache
		 WHERE expires_at IS NOT NULL AND expires_at < ?`,
		time.Now().Format(time.RFC3339))
	_ = row.Scan(&stats.ExpiredCount)

	return stats, nil
}

// Prune removes expired entries.
func (c *Cache) Prune(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := c.db.ExecContext(ctx,
		`DELETE FROM extraction_cache
		 WHERE expires_at IS NOT NULL AND expires_at < ?`,
		time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Close closes the cache database.
func (c *Cache) Close() error {
	return c.db.Close()
}

// CacheStats holds cache usage statistics.
type CacheStats struct {
	EntryCount   int     `json:"entry_count"`
	ExpiredCount int     `json:"expired_count"`
	TotalTokens  int     `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

func truncateKey(key string, maxLen int) string {
	if len(key) <= maxLen {
		return key
	}
	return key[:maxLen]
}

// --- Cached Extractor Wrapper ---

// CachedExtractor wraps an LLMExtractor with caching.
type CachedExtractor struct {
	inner  LLMExtractor
	cache  *Cache
	ttl    time.Duration
	logger *slog.Logger
}

// NewCachedExtractor wraps an extractor with caching.
func NewCachedExtractor(inner LLMExtractor, cache *Cache, ttl time.Duration, logger *slog.Logger) *CachedExtractor {
	return &CachedExtractor{
		inner:  inner,
		cache:  cache,
		ttl:    ttl,
		logger: logger.With("component", "cached_extractor"),
	}
}

func (e *CachedExtractor) Name() string { return e.inner.Name() + "+cache" }

// Extract checks the cache first, then calls the inner extractor on miss.
func (e *CachedExtractor) Extract(ctx context.Context, html string, schema ExtractionSchema) (map[string]any, error) {
	key := CacheKey(html, schema)

	// Check cache.
	entry, err := e.cache.Get(ctx, key)
	if err != nil {
		e.logger.Warn("cache get error", "error", err)
	}
	if entry != nil {
		e.logger.Debug("cache hit", "key", key[:16])
		return entry.Result, nil
	}

	// Cache miss — call the real extractor.
	result, err := e.inner.Extract(ctx, html, schema)
	if err != nil {
		return nil, err
	}

	// Store in cache.
	cacheEntry := &CacheEntry{
		Result:    result,
		Model:     e.inner.Name(),
		CreatedAt: time.Now(),
	}
	if err := e.cache.Put(ctx, key, cacheEntry, e.ttl); err != nil {
		e.logger.Warn("cache put error", "error", err)
	}

	return result, nil
}

// ExtractBatch processes pages with caching.
func (e *CachedExtractor) ExtractBatch(ctx context.Context, pages []string, schema ExtractionSchema) ([]map[string]any, error) {
	results := make([]map[string]any, len(pages))
	for i, page := range pages {
		result, err := e.Extract(ctx, page, schema)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i, err)
		}
		results[i] = result
	}
	return results, nil
}

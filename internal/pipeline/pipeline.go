package pipeline

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Middleware processes an item and returns the (possibly modified) item.
// Return nil to drop the item from the pipeline.
type Middleware interface {
	// Name returns the middleware's identifier.
	Name() string

	// Process transforms an item. Return nil to drop the item.
	Process(item *types.Item) (*types.Item, error)
}

// Pipeline chains middleware processors together.
type Pipeline struct {
	middlewares []Middleware
	logger      *slog.Logger
}

// New creates a new Pipeline.
func New(logger *slog.Logger) *Pipeline {
	return &Pipeline{
		logger: logger.With("component", "pipeline"),
	}
}

// Use adds a middleware to the pipeline chain.
func (p *Pipeline) Use(mw Middleware) {
	p.middlewares = append(p.middlewares, mw)
	p.logger.Debug("middleware added", "name", mw.Name(), "position", len(p.middlewares))
}

// Process runs the item through all middleware in order.
func (p *Pipeline) Process(item *types.Item) (*types.Item, error) {
	current := item

	for _, mw := range p.middlewares {
		result, err := mw.Process(current)
		if err != nil {
			return nil, &types.PipelineError{
				Stage: mw.Name(),
				Item:  current,
				Err:   err,
			}
		}
		if result == nil {
			// Item dropped by middleware
			p.logger.Debug("item dropped", "stage", mw.Name(), "url", item.URL)
			return nil, nil
		}
		current = result
	}

	return current, nil
}

// Len returns the number of middleware in the chain.
func (p *Pipeline) Len() int {
	return len(p.middlewares)
}

// --- Built-in Middleware ---

// FieldFilterMiddleware keeps only specified fields.
type FieldFilterMiddleware struct {
	Fields map[string]bool
}

func (m *FieldFilterMiddleware) Name() string { return "field_filter" }

func (m *FieldFilterMiddleware) Process(item *types.Item) (*types.Item, error) {
	if len(m.Fields) == 0 {
		return item, nil
	}
	for key := range item.Fields {
		if !m.Fields[key] {
			item.Delete(key)
		}
	}
	return item, nil
}

// FieldRenameMiddleware renames fields.
type FieldRenameMiddleware struct {
	Mapping map[string]string // old name -> new name
}

func (m *FieldRenameMiddleware) Name() string { return "field_rename" }

func (m *FieldRenameMiddleware) Process(item *types.Item) (*types.Item, error) {
	for oldKey, newKey := range m.Mapping {
		if val, ok := item.Get(oldKey); ok {
			item.Set(newKey, val)
			item.Delete(oldKey)
		}
	}
	return item, nil
}

// RequiredFieldsMiddleware drops items missing required fields.
type RequiredFieldsMiddleware struct {
	Fields []string
}

func (m *RequiredFieldsMiddleware) Name() string { return "required_fields" }

func (m *RequiredFieldsMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, field := range m.Fields {
		if !item.Has(field) {
			return nil, nil // Drop item
		}
		// Also drop if field is empty string
		if s := item.GetString(field); s == "" {
			val, _ := item.Get(field)
			if val == nil {
				return nil, nil
			}
		}
	}
	return item, nil
}

// DedupMiddleware drops items with duplicate checksums.
type DedupMiddleware struct {
	mu   sync.Mutex
	seen map[string]struct{}
	key  string // Field to use as dedup key
}

func NewDedupMiddleware(key string) *DedupMiddleware {
	return &DedupMiddleware{
		seen: make(map[string]struct{}),
		key:  key,
	}
}

func (m *DedupMiddleware) Name() string { return "dedup" }

func (m *DedupMiddleware) Process(item *types.Item) (*types.Item, error) {
	val := item.GetString(m.key)
	if val == "" {
		val = item.URL // Fallback to URL
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.seen[val]; exists {
		return nil, nil // Drop duplicate
	}
	m.seen[val] = struct{}{}
	return item, nil
}

// DefaultValueMiddleware sets default values for missing fields.
type DefaultValueMiddleware struct {
	Defaults map[string]any
}

func (m *DefaultValueMiddleware) Name() string { return "default_values" }

func (m *DefaultValueMiddleware) Process(item *types.Item) (*types.Item, error) {
	for key, defaultVal := range m.Defaults {
		if !item.Has(key) {
			item.Set(key, defaultVal)
		}
	}
	return item, nil
}

// TrimMiddleware trims whitespace from all string fields.
type TrimMiddleware struct{}

func (m *TrimMiddleware) Name() string { return "trim" }

func (m *TrimMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, key := range item.Keys() {
		if s := item.GetString(key); s != "" {
			item.Set(key, strings.TrimSpace(s))
		}
	}
	return item, nil
}

// Package plugin/sdk provides helper types and functions for building
// ScrapeGoat plugins using the init() registration pattern.
package sdk

import (
	"log/slog"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// BasePlugin provides a default implementation for the Plugin interface
// that plugin authors can embed to reduce boilerplate.
type BasePlugin struct {
	PluginName    string
	PluginType    string
	PluginVersion string
	Config        map[string]any
	Logger        *slog.Logger
}

// Name returns the plugin name.
func (p *BasePlugin) Name() string { return p.PluginName }

// Version returns the plugin version.
func (p *BasePlugin) Version() string { return p.PluginVersion }

// Init stores config and is a no-op by default.
func (p *BasePlugin) Init(cfg map[string]any) error {
	p.Config = cfg
	return nil
}

// Close is a no-op by default.
func (p *BasePlugin) Close() error { return nil }

// GetString extracts a string config value with a default.
func (p *BasePlugin) GetString(key, defaultVal string) string {
	if v, ok := p.Config[key].(string); ok {
		return v
	}
	return defaultVal
}

// GetInt extracts an integer config value with a default.
func (p *BasePlugin) GetInt(key string, defaultVal int) int {
	switch v := p.Config[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return defaultVal
	}
}

// GetBool extracts a boolean config value with a default.
func (p *BasePlugin) GetBool(key string, defaultVal bool) bool {
	if v, ok := p.Config[key].(bool); ok {
		return v
	}
	return defaultVal
}

// --- Helper Types ---

// ItemFilter is a function that returns true to keep an item.
type ItemFilter func(item *types.Item) bool

// ItemTransform is a function that modifies an item in place.
type ItemTransform func(item *types.Item)

// FilterMiddleware creates a pipeline middleware from a filter function.
type FilterMiddleware struct {
	FilterFn ItemFilter
	PluginName string
}

// Process applies the filter.
func (m *FilterMiddleware) Process(item *types.Item) (*types.Item, error) {
	if m.FilterFn(item) {
		return item, nil
	}
	return nil, nil // Drop the item.
}

// Name returns the middleware name.
func (m *FilterMiddleware) Name() string { return m.PluginName }

// TransformMiddleware creates a pipeline middleware from a transform function.
type TransformMiddleware struct {
	TransformFn ItemTransform
	PluginName  string
}

// Process applies the transform.
func (m *TransformMiddleware) Process(item *types.Item) (*types.Item, error) {
	m.TransformFn(item)
	return item, nil
}

// Name returns the middleware name.
func (m *TransformMiddleware) Name() string { return m.PluginName }

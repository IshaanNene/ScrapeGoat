package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// PluginType identifies the category of a plugin.
type PluginType string

const (
	PluginTypeFetcher    PluginType = "fetcher"
	PluginTypeParser     PluginType = "parser"
	PluginTypeMiddleware PluginType = "middleware"
	PluginTypeStorage    PluginType = "storage"
	PluginTypeHook       PluginType = "hook"
)

// Plugin is the base interface all plugins must implement.
type Plugin interface {
	Name() string
	Type() PluginType
	Version() string
	Init(cfg map[string]any) error
	Close() error
}

// FetcherPlugin extends the fetcher layer.
type FetcherPlugin interface {
	Plugin
	Fetch(ctx context.Context, req *types.Request) (*types.Response, error)
}

// ParserPlugin extends the parser layer.
type ParserPlugin interface {
	Plugin
	Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error)
}

// MiddlewarePlugin extends the pipeline.
type MiddlewarePlugin interface {
	Plugin
	Process(item *types.Item) (*types.Item, error)
}

// StoragePlugin extends storage backends.
type StoragePlugin interface {
	Plugin
	Store(items []*types.Item) error
}

// HookPlugin receives lifecycle events.
type HookPlugin interface {
	Plugin
	OnStart() error
	OnStop() error
	OnRequest(req *types.Request) error
	OnResponse(resp *types.Response) error
	OnItem(item *types.Item) error
	OnError(err error)
}

// --- Plugin Registry ---

// Registry manages plugin registration and discovery.
type Registry struct {
	plugins map[string]Plugin
	byType  map[PluginType][]Plugin
	logger  *slog.Logger
	mu      sync.RWMutex
}

// NewRegistry creates a new plugin registry.
func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		plugins: make(map[string]Plugin),
		byType:  make(map[PluginType][]Plugin),
		logger:  logger.With("component", "plugin_registry"),
	}
}

// Register adds a plugin to the registry.
func (r *Registry) Register(p Plugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := p.Name()
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("plugin %q already registered", name)
	}

	r.plugins[name] = p
	r.byType[p.Type()] = append(r.byType[p.Type()], p)

	r.logger.Info("plugin registered",
		"name", name,
		"type", p.Type(),
		"version", p.Version(),
	)
	return nil
}

// Get returns a plugin by name.
func (r *Registry) Get(name string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[name]
	return p, ok
}

// GetByType returns all plugins of a given type.
func (r *Registry) GetByType(t PluginType) []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byType[t]
}

// Unregister removes a plugin from the registry.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}

	// Close the plugin
	if err := p.Close(); err != nil {
		r.logger.Warn("plugin close error", "name", name, "error", err)
	}

	// Remove from type index
	plugins := r.byType[p.Type()]
	for i, pl := range plugins {
		if pl.Name() == name {
			r.byType[p.Type()] = append(plugins[:i], plugins[i+1:]...)
			break
		}
	}

	delete(r.plugins, name)
	r.logger.Info("plugin unregistered", "name", name)
	return nil
}

// List returns all registered plugins.
func (r *Registry) List() []PluginInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]PluginInfo, 0, len(r.plugins))
	for _, p := range r.plugins {
		infos = append(infos, PluginInfo{
			Name:    p.Name(),
			Type:    p.Type(),
			Version: p.Version(),
		})
	}
	return infos
}

// PluginInfo holds summary information about a plugin.
type PluginInfo struct {
	Name    string     `json:"name"`
	Type    PluginType `json:"type"`
	Version string     `json:"version"`
}

// InitAll initializes all registered plugins with their configs.
func (r *Registry) InitAll(configs map[string]map[string]any) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, p := range r.plugins {
		cfg := configs[name]
		if err := p.Init(cfg); err != nil {
			return fmt.Errorf("init plugin %q: %w", name, err)
		}
	}
	return nil
}

// CloseAll closes all registered plugins.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for name, p := range r.plugins {
		if err := p.Close(); err != nil {
			r.logger.Error("plugin close error", "name", name, "error", err)
		}
	}
}

// RunHooks executes a hook function on all HookPlugins.
func (r *Registry) RunHooks(fn func(HookPlugin) error) {
	hooks := r.GetByType(PluginTypeHook)
	for _, p := range hooks {
		if hook, ok := p.(HookPlugin); ok {
			if err := fn(hook); err != nil {
				r.logger.Warn("hook error", "plugin", p.Name(), "error", err)
			}
		}
	}
}

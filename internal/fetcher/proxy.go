package fetcher

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
)

// ProxyManager handles proxy rotation and health checking.
type ProxyManager struct {
	proxies  []*proxyEntry
	rotation string
	index    atomic.Int64
	mu       sync.RWMutex
	logger   *slog.Logger
}

type proxyEntry struct {
	URL     *url.URL
	Healthy bool
	LastErr error
	LastUse time.Time
	mu      sync.Mutex
}

// NewProxyManager creates a new ProxyManager from configuration.
func NewProxyManager(cfg *config.ProxyConfig, logger *slog.Logger) *ProxyManager {
	pm := &ProxyManager{
		proxies:  make([]*proxyEntry, 0, len(cfg.URLs)),
		rotation: cfg.Rotation,
		logger:   logger.With("component", "proxy_manager"),
	}

	for _, rawURL := range cfg.URLs {
		u, err := url.Parse(rawURL)
		if err != nil {
			logger.Warn("invalid proxy URL", "url", rawURL, "error", err)
			continue
		}
		pm.proxies = append(pm.proxies, &proxyEntry{
			URL:     u,
			Healthy: true,
		})
	}

	logger.Info("proxy manager initialized", "count", len(pm.proxies), "rotation", cfg.Rotation)
	return pm
}

// ProxyFunc returns an http.Transport-compatible proxy function.
func (pm *ProxyManager) ProxyFunc() func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		proxy := pm.Next()
		if proxy == nil {
			return nil, nil // No proxy = direct connection
		}
		return proxy, nil
	}
}

// Next returns the next proxy URL based on the rotation strategy.
func (pm *ProxyManager) Next() *url.URL {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	healthy := pm.healthyProxies()
	if len(healthy) == 0 {
		return nil
	}

	switch pm.rotation {
	case "random":
		entry := healthy[rand.Intn(len(healthy))]
		entry.mu.Lock()
		entry.LastUse = time.Now()
		entry.mu.Unlock()
		return entry.URL
	default: // round_robin
		idx := pm.index.Add(1) % int64(len(healthy))
		entry := healthy[idx]
		entry.mu.Lock()
		entry.LastUse = time.Now()
		entry.mu.Unlock()
		return entry.URL
	}
}

// MarkFailed marks a proxy as unhealthy.
func (pm *ProxyManager) MarkFailed(proxyURL *url.URL, err error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, p := range pm.proxies {
		if p.URL.String() == proxyURL.String() {
			p.mu.Lock()
			p.Healthy = false
			p.LastErr = err
			p.mu.Unlock()
			pm.logger.Warn("proxy marked unhealthy",
				"proxy", proxyURL.Host,
				"error", err,
			)
			break
		}
	}
}

// MarkHealthy marks a proxy as healthy.
func (pm *ProxyManager) MarkHealthy(proxyURL *url.URL) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, p := range pm.proxies {
		if p.URL.String() == proxyURL.String() {
			p.mu.Lock()
			p.Healthy = true
			p.LastErr = nil
			p.mu.Unlock()
			break
		}
	}
}

// HealthCheck pings all proxies and updates their status.
func (pm *ProxyManager) HealthCheck() {
	pm.mu.RLock()
	proxies := make([]*proxyEntry, len(pm.proxies))
	copy(proxies, pm.proxies)
	pm.mu.RUnlock()

	client := &http.Client{Timeout: 10 * time.Second}

	for _, p := range proxies {
		transport := &http.Transport{
			Proxy: http.ProxyURL(p.URL),
		}
		client.Transport = transport

		resp, err := client.Get("https://httpbin.org/ip")
		if err != nil {
			pm.MarkFailed(p.URL, err)
		} else {
			resp.Body.Close()
			pm.MarkHealthy(p.URL)
		}
	}
}

// Count returns the total number of proxies.
func (pm *ProxyManager) Count() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.proxies)
}

// HealthyCount returns the number of healthy proxies.
func (pm *ProxyManager) HealthyCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.healthyProxies())
}

// AddProxy adds a new proxy URL at runtime.
func (pm *ProxyManager) AddProxy(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.proxies = append(pm.proxies, &proxyEntry{
		URL:     u,
		Healthy: true,
	})
	return nil
}

func (pm *ProxyManager) healthyProxies() []*proxyEntry {
	healthy := make([]*proxyEntry, 0, len(pm.proxies))
	for _, p := range pm.proxies {
		p.mu.Lock()
		if p.Healthy {
			healthy = append(healthy, p)
		}
		p.mu.Unlock()
	}
	return healthy
}

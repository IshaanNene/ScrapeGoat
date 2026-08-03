package engine

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/safety"
)

// RobotsManager handles robots.txt fetching, parsing, and enforcement.
type RobotsManager struct {
	enabled bool
	cache   map[string]*robotsData
	mu      sync.RWMutex
	client  *http.Client
}

// robotsData holds parsed robots.txt rules for a domain.
type robotsData struct {
	disallowed []string
	allowed    []string
	crawlDelay time.Duration
	sitemaps   []string
	fetchedAt  time.Time
}

// NewRobotsManager creates a new RobotsManager.
//
// guard may be nil, in which case the default policy applies. The robots.txt URL is
// derived from the crawl target's own host, so it is exactly as untrusted as the
// page it governs and needs the same guarded client.
func NewRobotsManager(enabled bool, guard *safety.URLGuard) *RobotsManager {
	if guard == nil {
		guard = safety.Default()
	}
	return &RobotsManager{
		enabled: enabled,
		cache:   make(map[string]*robotsData),
		client:  guard.HTTPClient(10*time.Second, 5),
	}
}

// IsAllowed checks if a URL is allowed by the domain's robots.txt.
func (rm *RobotsManager) IsAllowed(rawURL string) bool {
	if !rm.enabled {
		return true
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}

	domain := u.Scheme + "://" + u.Host
	data := rm.getRobotsData(domain)
	if data == nil {
		return true // Can't fetch robots.txt = allow
	}

	path := u.Path
	if path == "" {
		path = "/"
	}

	// Check allowed rules first (they override disallowed)
	for _, pattern := range data.allowed {
		if matchRobotsPattern(pattern, path) {
			return true
		}
	}

	// Check disallowed rules
	for _, pattern := range data.disallowed {
		if matchRobotsPattern(pattern, path) {
			return false
		}
	}

	return true
}

// GetCrawlDelay returns the crawl-delay for a domain, if specified.
func (rm *RobotsManager) GetCrawlDelay(domain string) time.Duration {
	rm.mu.RLock()
	data, ok := rm.cache[domain]
	rm.mu.RUnlock()

	if !ok || data == nil {
		return 0
	}
	return data.crawlDelay
}

// GetSitemaps returns the sitemaps listed in robots.txt for a domain.
func (rm *RobotsManager) GetSitemaps(domain string) []string {
	rm.mu.RLock()
	data, ok := rm.cache[domain]
	rm.mu.RUnlock()

	if !ok || data == nil {
		return nil
	}
	return data.sitemaps
}

// getRobotsData fetches and caches robots.txt for a domain.
func (rm *RobotsManager) getRobotsData(domain string) *robotsData {
	rm.mu.RLock()
	data, ok := rm.cache[domain]
	rm.mu.RUnlock()

	if ok {
		return data
	}

	// Fetch robots.txt
	data = rm.fetchRobotsTxt(domain)

	rm.mu.Lock()
	rm.cache[domain] = data
	rm.mu.Unlock()

	return data
}

// fetchRobotsTxt downloads and parses robots.txt.
func (rm *RobotsManager) fetchRobotsTxt(domain string) *robotsData {
	robotsURL := domain + "/robots.txt"

	resp, err := rm.client.Get(robotsURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // 512KB limit
	if err != nil {
		return nil
	}

	return parseRobotsTxt(string(body))
}

// parseRobotsTxt parses robots.txt content.
func parseRobotsTxt(content string) *robotsData {
	data := &robotsData{
		fetchedAt: time.Now(),
	}

	lines := strings.Split(content, "\n")
	inOurSection := false
	userAgent := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Remove comments
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}

		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(parts[0]))
		value := strings.TrimSpace(parts[1])

		switch key {
		case "user-agent":
			userAgent = strings.ToLower(value)
			inOurSection = (userAgent == "*" || strings.Contains(userAgent, "scrapegoat"))
		case "disallow":
			if inOurSection && value != "" {
				data.disallowed = append(data.disallowed, value)
			}
		case "allow":
			if inOurSection && value != "" {
				data.allowed = append(data.allowed, value)
			}
		case "crawl-delay":
			if inOurSection {
				var delay float64
				if _, err := fmt.Sscanf(value, "%f", &delay); err == nil {
					data.crawlDelay = time.Duration(delay * float64(time.Second))
				}
			}
		case "sitemap":
			data.sitemaps = append(data.sitemaps, value)
		}
	}

	return data
}

// matchRobotsPattern checks if a URL path matches a robots.txt pattern.
// Supports * (any sequence) and $ (end of URL) wildcards.
func matchRobotsPattern(pattern, path string) bool {
	if pattern == "" {
		return false
	}

	// Handle $ anchor at end
	endsWithDollar := strings.HasSuffix(pattern, "$")
	if endsWithDollar {
		pattern = pattern[:len(pattern)-1]
	}

	// Handle * wildcards
	if strings.Contains(pattern, "*") {
		return matchWildcard(pattern, path, endsWithDollar)
	}

	// Simple prefix match
	if endsWithDollar {
		return path == pattern
	}
	return strings.HasPrefix(path, pattern)
}

// matchWildcard handles * wildcard matching in robots.txt patterns.
func matchWildcard(pattern, path string, mustEnd bool) bool {
	parts := strings.Split(pattern, "*")
	pos := 0

	for i, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(path[pos:], part)
		if idx < 0 {
			return false
		}
		if i == 0 && idx != 0 {
			// First part must match from the start
			return false
		}
		pos += idx + len(part)
	}

	if mustEnd {
		return pos == len(path)
	}
	return true
}

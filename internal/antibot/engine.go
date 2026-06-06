// Package antibot implements an adaptive anti-bot engine with block detection,
// stealth profiles, and human behaviour simulation. Detection patterns are
// extensible via a registry pattern — not hardcoded strings.
package antibot

import (
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// BlockReason classifies why a response was blocked.
type BlockReason int

const (
	NotBlocked BlockReason = iota
	RateLimit
	Cloudflare
	Captcha
	IPBan
	HoneypotTriggered
	ContentBlocked
	DataDome
	PerimeterX
	Akamai
)

// String returns a human-readable name for the block reason.
func (b BlockReason) String() string {
	names := [...]string{
		"not_blocked", "rate_limit", "cloudflare", "captcha",
		"ip_ban", "honeypot", "content_blocked", "datadome",
		"perimeterx", "akamai",
	}
	if int(b) < len(names) {
		return names[b]
	}
	return "unknown"
}

// DetectionResult holds the result of block detection.
type DetectionResult struct {
	Blocked bool        `json:"blocked"`
	Reason  BlockReason `json:"reason"`
	Score   float64     `json:"score"` // 0.0 = definitely not blocked, 1.0 = definitely blocked
	Details string      `json:"details"`
}

// DetectionPattern defines a single detection rule.
// Patterns are matched against response headers, status codes, and body content.
type DetectionPattern struct {
	Name        string
	Reason      BlockReason
	StatusCodes []int
	Headers     map[string]*regexp.Regexp
	BodyPattern *regexp.Regexp
	Weight      float64 // 0.0 - 1.0 contribution to block score
}

// BlockDetector classifies HTTP responses as blocked or not.
type BlockDetector struct {
	patterns []DetectionPattern
	logger   *slog.Logger
	mu       sync.RWMutex
}

// NewBlockDetector creates a detector with built-in patterns.
func NewBlockDetector(logger *slog.Logger) *BlockDetector {
	d := &BlockDetector{
		logger: logger.With("component", "block_detector"),
	}
	d.registerDefaults()
	return d
}

// RegisterPattern adds a custom detection pattern.
func (d *BlockDetector) RegisterPattern(p DetectionPattern) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.patterns = append(d.patterns, p)
	d.logger.Debug("registered detection pattern", "name", p.Name, "reason", p.Reason.String())
}

// Detect analyzes a response and returns whether it appears to be blocked.
func (d *BlockDetector) Detect(resp *types.Response) DetectionResult {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := DetectionResult{Blocked: false, Reason: NotBlocked}
	var totalScore float64
	var matchCount int

	for _, pattern := range d.patterns {
		score := d.matchPattern(resp, pattern)
		if score > 0 {
			totalScore += score * pattern.Weight
			matchCount++
			if score > 0.5 {
				result.Reason = pattern.Reason
				result.Details = pattern.Name
			}
		}
	}

	if matchCount > 0 {
		result.Score = totalScore / float64(matchCount)
		if result.Score > 0.5 {
			result.Score = min(1.0, totalScore)
		}
	}

	result.Blocked = result.Score >= 0.6
	return result
}

func (d *BlockDetector) matchPattern(resp *types.Response, p DetectionPattern) float64 {
	var score float64

	// Check status code.
	if len(p.StatusCodes) > 0 {
		for _, code := range p.StatusCodes {
			if resp.StatusCode == code {
				score += 0.5
				break
			}
		}
	}

	// Check headers.
	for headerName, pattern := range p.Headers {
		for key, values := range resp.Headers {
			if !strings.EqualFold(key, headerName) {
				continue
			}
			for _, v := range values {
				if pattern.MatchString(v) {
					score += 0.5
				}
			}
		}
	}

	// Check body content.
	if p.BodyPattern != nil && len(resp.Body) > 0 {
		body := string(resp.Body)
		if len(body) > 50000 {
			body = body[:50000] // Limit scan size.
		}
		if p.BodyPattern.MatchString(body) {
			score += 0.5
		}
	}

	return min(score, 1.0)
}

// registerDefaults adds the built-in extensible detection patterns.
func (d *BlockDetector) registerDefaults() {
	// Cloudflare patterns.
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "cloudflare_challenge",
		Reason:      Cloudflare,
		StatusCodes: []int{403, 503},
		Headers:     map[string]*regexp.Regexp{"cf-ray": regexp.MustCompile(`.+`), "server": regexp.MustCompile(`(?i)cloudflare`)},
		BodyPattern: regexp.MustCompile(`(?i)(cf-browser-verification|challenge-platform|cloudflare|ray\s*id)`),
		Weight:      0.9,
	})

	// CAPTCHA patterns.
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "captcha_challenge",
		Reason:      Captcha,
		BodyPattern: regexp.MustCompile(`(?i)(recaptcha|hcaptcha|captcha-form|g-recaptcha|cf-turnstile|captcha_challenge)`),
		Weight:      0.95,
	})

	// Rate limiting (generic).
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "rate_limit",
		Reason:      RateLimit,
		StatusCodes: []int{429},
		Headers:     map[string]*regexp.Regexp{"retry-after": regexp.MustCompile(`.+`)},
		Weight:      0.9,
	})

	// IP ban patterns.
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "ip_ban",
		Reason:      IPBan,
		StatusCodes: []int{403, 451},
		BodyPattern: regexp.MustCompile(`(?i)(your\s+(ip|access)\s+(has been|is)\s+(blocked|banned|denied)|ip\s+address\s+blocked)`),
		Weight:      0.85,
	})

	// DataDome patterns.
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "datadome_protection",
		Reason:      DataDome,
		Headers:     map[string]*regexp.Regexp{"x-datadome": regexp.MustCompile(`.+`), "server": regexp.MustCompile(`(?i)datadome`)},
		BodyPattern: regexp.MustCompile(`(?i)(datadome|dd\.datadome|geo\.captcha-delivery)`),
		Weight:      0.9,
	})

	// PerimeterX patterns.
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "perimeterx_protection",
		Reason:      PerimeterX,
		Headers:     map[string]*regexp.Regexp{"x-px": regexp.MustCompile(`.+`)},
		BodyPattern: regexp.MustCompile(`(?i)(_pxhd|px-captcha|perimeterx|px-block|human-challenge)`),
		Weight:      0.9,
	})

	// Akamai patterns.
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "akamai_bot_manager",
		Reason:      Akamai,
		Headers:     map[string]*regexp.Regexp{"x-akamai-transformed": regexp.MustCompile(`.+`), "server": regexp.MustCompile(`(?i)akamai`)},
		BodyPattern: regexp.MustCompile(`(?i)(akamai|ak_bmsc|bm_sz|akam/|sensor_data)`),
		Weight:      0.85,
	})

	// Empty body on normally content-serving pages.
	d.patterns = append(d.patterns, DetectionPattern{
		Name:        "content_blocked",
		Reason:      ContentBlocked,
		StatusCodes: []int{200},
		BodyPattern: regexp.MustCompile(`^$`),
		Weight:      0.3,
	})
}

// --- Adaptive Strategy ---

// DomainStats tracks block rates per domain.
type DomainStats struct {
	Domain     string
	Total      int
	Blocked    int
	LastBlock  time.Time
	PausedUntil time.Time
}

// AdaptiveStrategy adjusts scraping behavior based on block rates.
type AdaptiveStrategy struct {
	domains  map[string]*DomainStats
	detector *BlockDetector
	logger   *slog.Logger
	mu       sync.Mutex
}

// NewAdaptiveStrategy creates an adaptive anti-bot strategy.
func NewAdaptiveStrategy(detector *BlockDetector, logger *slog.Logger) *AdaptiveStrategy {
	return &AdaptiveStrategy{
		domains:  make(map[string]*DomainStats),
		detector: detector,
		logger:   logger.With("component", "adaptive_strategy"),
	}
}

// RecordResponse records a response and returns recommended action.
func (s *AdaptiveStrategy) RecordResponse(domain string, resp *types.Response) Action {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats, ok := s.domains[domain]
	if !ok {
		stats = &DomainStats{Domain: domain}
		s.domains[domain] = stats
	}

	// Check if domain is paused.
	if time.Now().Before(stats.PausedUntil) {
		return Action{Type: ActionPause, Wait: time.Until(stats.PausedUntil)}
	}

	detection := s.detector.Detect(resp)
	stats.Total++

	if detection.Blocked {
		stats.Blocked++
		stats.LastBlock = time.Now()

		blockRate := float64(stats.Blocked) / float64(stats.Total)

		s.logger.Warn("block detected",
			"domain", domain,
			"reason", detection.Reason.String(),
			"block_rate", blockRate,
		)

		// If >50% block rate, pause the domain.
		if blockRate > 0.5 && stats.Total >= 5 {
			pauseDuration := 30 * time.Second
			stats.PausedUntil = time.Now().Add(pauseDuration)
			return Action{
				Type: ActionPauseDomain,
				Wait: pauseDuration,
			}
		}

		// Escalate strategies based on block reason.
		switch detection.Reason {
		case RateLimit:
			return Action{Type: ActionSlowDown, Wait: 5 * time.Second}
		case Cloudflare, DataDome, PerimeterX, Akamai:
			return Action{Type: ActionUseBrowser}
		case Captcha:
			return Action{Type: ActionRotateProxy}
		case IPBan:
			return Action{Type: ActionRotateProxy}
		default:
			return Action{Type: ActionBackoff, Wait: 2 * time.Second}
		}
	}

	return Action{Type: ActionContinue}
}

// GetDomainStats returns stats for a domain.
func (s *AdaptiveStrategy) GetDomainStats(domain string) *DomainStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.domains[domain]
}

// Action represents what the engine should do after a response.
type Action struct {
	Type ActionType
	Wait time.Duration
}

// ActionType classifies the recommended action.
type ActionType int

const (
	ActionContinue    ActionType = iota
	ActionSlowDown
	ActionBackoff
	ActionRotateProxy
	ActionUseBrowser
	ActionPause
	ActionPauseDomain
)

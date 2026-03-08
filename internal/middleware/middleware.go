package middleware

import (
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// RequestMiddleware intercepts requests before/after fetching.
// This is the core framework extension point — users plug into the
// request lifecycle to add proxy rotation, retries, anti-bot measures, etc.
//
// Example:
//
//	type MyMiddleware struct{}
//
//	func (m *MyMiddleware) Name() string { return "my_middleware" }
//	func (m *MyMiddleware) Priority() int { return 100 }
//
//	func (m *MyMiddleware) ProcessRequest(req *types.Request) *types.Request {
//	    req.Headers.Set("X-Custom", "value")
//	    return req
//	}
//
//	func (m *MyMiddleware) ProcessResponse(resp *types.Response) *types.Response {
//	    return resp
//	}
type RequestMiddleware interface {
	// Name returns a human-readable identifier.
	Name() string

	// Priority controls execution order (lower = runs first).
	Priority() int

	// ProcessRequest is called before the fetcher. Modify the request or return nil to drop.
	ProcessRequest(req *types.Request) *types.Request

	// ProcessResponse is called after the fetcher. Modify or inspect the response.
	ProcessResponse(resp *types.Response) *types.Response
}

// RequestPipeline chains request middlewares in priority order.
type RequestPipeline struct {
	middlewares []RequestMiddleware
	logger      *slog.Logger
	mu          sync.RWMutex
}

// NewRequestPipeline creates a new request middleware pipeline.
func NewRequestPipeline(logger *slog.Logger) *RequestPipeline {
	return &RequestPipeline{
		logger: logger.With("component", "request_pipeline"),
	}
}

// Use adds a middleware to the pipeline (sorted by priority).
func (rp *RequestPipeline) Use(mw RequestMiddleware) {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	rp.middlewares = append(rp.middlewares, mw)
	// Sort by priority (insertion sort — middlewares list is small)
	for i := len(rp.middlewares) - 1; i > 0; i-- {
		if rp.middlewares[i].Priority() < rp.middlewares[i-1].Priority() {
			rp.middlewares[i], rp.middlewares[i-1] = rp.middlewares[i-1], rp.middlewares[i]
		}
	}

	rp.logger.Debug("middleware added", "name", mw.Name(), "priority", mw.Priority())
}

// ProcessRequest runs all middlewares on a request (before fetch).
func (rp *RequestPipeline) ProcessRequest(req *types.Request) *types.Request {
	rp.mu.RLock()
	mws := make([]RequestMiddleware, len(rp.middlewares))
	copy(mws, rp.middlewares)
	rp.mu.RUnlock()

	current := req
	for _, mw := range mws {
		current = mw.ProcessRequest(current)
		if current == nil {
			rp.logger.Debug("request dropped by middleware", "middleware", mw.Name())
			return nil
		}
	}
	return current
}

// ProcessResponse runs all middlewares on a response (after fetch), in reverse order.
func (rp *RequestPipeline) ProcessResponse(resp *types.Response) *types.Response {
	rp.mu.RLock()
	mws := make([]RequestMiddleware, len(rp.middlewares))
	copy(mws, rp.middlewares)
	rp.mu.RUnlock()

	current := resp
	for i := len(mws) - 1; i >= 0; i-- {
		current = mws[i].ProcessResponse(current)
		if current == nil {
			return nil
		}
	}
	return current
}

// Len returns the number of middlewares in the pipeline.
func (rp *RequestPipeline) Len() int {
	rp.mu.RLock()
	defer rp.mu.RUnlock()
	return len(rp.middlewares)
}

// --- Built-in Middlewares ---

// HeaderRotationMiddleware rotates request headers to mimic different browsers.
type HeaderRotationMiddleware struct {
	acceptHeaders []string
	languages     []string
	index         atomic.Int64
}

// NewHeaderRotationMiddleware creates a header rotation middleware.
func NewHeaderRotationMiddleware() *HeaderRotationMiddleware {
	return &HeaderRotationMiddleware{
		acceptHeaders: []string{
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8",
			"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		},
		languages: []string{
			"en-US,en;q=0.9",
			"en-US,en;q=0.9,es;q=0.8",
			"en-GB,en;q=0.9,en-US;q=0.8",
			"en-US,en;q=0.5",
			"en-US,en;q=0.9,fr;q=0.8,de;q=0.7",
		},
	}
}

func (m *HeaderRotationMiddleware) Name() string  { return "header_rotation" }
func (m *HeaderRotationMiddleware) Priority() int { return 100 }

func (m *HeaderRotationMiddleware) ProcessRequest(req *types.Request) *types.Request {
	idx := m.index.Add(1)
	if req.Headers.Get("Accept") == "" {
		req.Headers.Set("Accept", m.acceptHeaders[idx%int64(len(m.acceptHeaders))])
	}
	if req.Headers.Get("Accept-Language") == "" {
		req.Headers.Set("Accept-Language", m.languages[idx%int64(len(m.languages))])
	}
	if req.Headers.Get("Accept-Encoding") == "" {
		req.Headers.Set("Accept-Encoding", "gzip, deflate, br")
	}
	if req.Headers.Get("Connection") == "" {
		req.Headers.Set("Connection", "keep-alive")
	}
	if req.Headers.Get("Upgrade-Insecure-Requests") == "" {
		req.Headers.Set("Upgrade-Insecure-Requests", "1")
	}
	return req
}

func (m *HeaderRotationMiddleware) ProcessResponse(resp *types.Response) *types.Response {
	return resp
}

// RetryMiddleware adds intelligent retry logic with exponential backoff.
type RetryMiddleware struct {
	MaxRetries int
	BaseDelay  time.Duration
}

func NewRetryMiddleware(maxRetries int, baseDelay time.Duration) *RetryMiddleware {
	return &RetryMiddleware{MaxRetries: maxRetries, BaseDelay: baseDelay}
}

func (m *RetryMiddleware) Name() string  { return "retry" }
func (m *RetryMiddleware) Priority() int { return 50 }

func (m *RetryMiddleware) ProcessRequest(req *types.Request) *types.Request {
	if req.MaxRetries == 0 {
		req.MaxRetries = m.MaxRetries
	}
	return req
}

func (m *RetryMiddleware) ProcessResponse(resp *types.Response) *types.Response {
	return resp
}

// RateLimitMiddleware enforces global rate limiting across all requests.
type RateLimitMiddleware struct {
	interval time.Duration
	mu       sync.Mutex
	lastReq  time.Time
}

// NewRateLimitMiddleware creates a rate limiter with the given interval between requests.
func NewRateLimitMiddleware(requestsPerSecond float64) *RateLimitMiddleware {
	interval := time.Duration(float64(time.Second) / requestsPerSecond)
	return &RateLimitMiddleware{interval: interval}
}

func (m *RateLimitMiddleware) Name() string  { return "rate_limit" }
func (m *RateLimitMiddleware) Priority() int { return 10 }

func (m *RateLimitMiddleware) ProcessRequest(req *types.Request) *types.Request {
	m.mu.Lock()
	defer m.mu.Unlock()

	elapsed := time.Since(m.lastReq)
	if elapsed < m.interval {
		time.Sleep(m.interval - elapsed)
	}
	m.lastReq = time.Now()
	return req
}

func (m *RateLimitMiddleware) ProcessResponse(resp *types.Response) *types.Response {
	return resp
}

// RequestFingerprintMiddleware diversifies request fingerprints to avoid detection.
type RequestFingerprintMiddleware struct {
	secChUa []string
}

// NewRequestFingerprintMiddleware creates a fingerprint diversification middleware.
func NewRequestFingerprintMiddleware() *RequestFingerprintMiddleware {
	return &RequestFingerprintMiddleware{
		secChUa: []string{
			`"Chromium";v="120", "Not?A_Brand";v="8", "Google Chrome";v="120"`,
			`"Chromium";v="121", "Not A(Brand";v="99", "Google Chrome";v="121"`,
			`"Chromium";v="119", "Not?A_Brand";v="24", "Google Chrome";v="119"`,
			`"Not_A Brand";v="8", "Chromium";v="120", "Microsoft Edge";v="120"`,
			`"Firefox";v="121"`,
		},
	}
}

func (m *RequestFingerprintMiddleware) Name() string  { return "request_fingerprint" }
func (m *RequestFingerprintMiddleware) Priority() int { return 90 }

func (m *RequestFingerprintMiddleware) ProcessRequest(req *types.Request) *types.Request {
	if req.Headers.Get("Sec-Ch-Ua") == "" {
		ua := m.secChUa[rand.Intn(len(m.secChUa))]
		req.Headers.Set("Sec-Ch-Ua", ua)
		req.Headers.Set("Sec-Ch-Ua-Mobile", "?0")

		platforms := []string{`"Windows"`, `"macOS"`, `"Linux"`}
		req.Headers.Set("Sec-Ch-Ua-Platform", platforms[rand.Intn(len(platforms))])
	}

	if req.Headers.Get("Sec-Fetch-Dest") == "" {
		req.Headers.Set("Sec-Fetch-Dest", "document")
		req.Headers.Set("Sec-Fetch-Mode", "navigate")
		req.Headers.Set("Sec-Fetch-Site", "none")
		req.Headers.Set("Sec-Fetch-User", "?1")
	}

	return req
}

func (m *RequestFingerprintMiddleware) ProcessResponse(resp *types.Response) *types.Response {
	return resp
}

// CaptchaDetectionMiddleware detects CAPTCHA pages in responses.
type CaptchaDetectionMiddleware struct {
	logger  *slog.Logger
	markers []string
}

// NewCaptchaDetectionMiddleware creates a CAPTCHA detection middleware.
func NewCaptchaDetectionMiddleware(logger *slog.Logger) *CaptchaDetectionMiddleware {
	return &CaptchaDetectionMiddleware{
		logger: logger,
		markers: []string{
			"g-recaptcha",
			"h-captcha",
			"cf-turnstile",
			"captcha",
			"recaptcha/api",
			"hcaptcha.com",
			"challenges.cloudflare.com",
		},
	}
}

func (m *CaptchaDetectionMiddleware) Name() string  { return "captcha_detection" }
func (m *CaptchaDetectionMiddleware) Priority() int { return 200 }

func (m *CaptchaDetectionMiddleware) ProcessRequest(req *types.Request) *types.Request {
	return req
}

func (m *CaptchaDetectionMiddleware) ProcessResponse(resp *types.Response) *types.Response {
	if resp == nil || len(resp.Body) == 0 {
		return resp
	}

	bodyStr := strings.ToLower(string(resp.Body))
	for _, marker := range m.markers {
		if strings.Contains(bodyStr, marker) {
			resp.Meta["captcha_detected"] = true
			resp.Meta["captcha_type"] = marker
			m.logger.Warn("CAPTCHA detected",
				"url", resp.Request.URLString(),
				"marker", marker,
			)
			break
		}
	}
	return resp
}

// ProxyRotationMiddleware integrates with proxy manager for per-request proxy selection.
type ProxyRotationMiddleware struct {
	proxies  []string
	index    atomic.Int64
	rotation string
}

// NewProxyRotationMiddleware creates a proxy rotation middleware.
func NewProxyRotationMiddleware(proxies []string, rotation string) *ProxyRotationMiddleware {
	return &ProxyRotationMiddleware{
		proxies:  proxies,
		rotation: rotation,
	}
}

func (m *ProxyRotationMiddleware) Name() string  { return "proxy_rotation" }
func (m *ProxyRotationMiddleware) Priority() int { return 20 }

func (m *ProxyRotationMiddleware) ProcessRequest(req *types.Request) *types.Request {
	if len(m.proxies) == 0 {
		return req
	}

	var proxy string
	switch m.rotation {
	case "random":
		proxy = m.proxies[rand.Intn(len(m.proxies))]
	default: // round_robin
		idx := m.index.Add(1) % int64(len(m.proxies))
		proxy = m.proxies[idx]
	}

	if req.Meta == nil {
		req.Meta = make(map[string]any)
	}
	req.Meta["proxy"] = proxy
	return req
}

func (m *ProxyRotationMiddleware) ProcessResponse(resp *types.Response) *types.Response {
	return resp
}

// CloudflareDetectionMiddleware detects Cloudflare challenges.
type CloudflareDetectionMiddleware struct {
	logger *slog.Logger
}

// NewCloudflareDetectionMiddleware creates a Cloudflare detection middleware.
func NewCloudflareDetectionMiddleware(logger *slog.Logger) *CloudflareDetectionMiddleware {
	return &CloudflareDetectionMiddleware{logger: logger}
}

func (m *CloudflareDetectionMiddleware) Name() string  { return "cloudflare_detection" }
func (m *CloudflareDetectionMiddleware) Priority() int { return 210 }

func (m *CloudflareDetectionMiddleware) ProcessRequest(req *types.Request) *types.Request {
	return req
}

func (m *CloudflareDetectionMiddleware) ProcessResponse(resp *types.Response) *types.Response {
	if resp == nil {
		return resp
	}

	// Detect Cloudflare challenge
	cfChallenge := false
	if resp.StatusCode == 403 || resp.StatusCode == 503 {
		bodyStr := strings.ToLower(string(resp.Body))
		cfMarkers := []string{
			"cloudflare",
			"cf-browser-verification",
			"jschl-answer",
			"cf_chl_opt",
			"_cf_chl_tk",
			"challenge-platform",
			"ray id",
		}
		for _, marker := range cfMarkers {
			if strings.Contains(bodyStr, marker) {
				cfChallenge = true
				break
			}
		}
	}

	// Check CF headers
	if resp.Headers != nil {
		if resp.Headers.Get("Cf-Ray") != "" || resp.Headers.Get("Server") == "cloudflare" {
			if resp.StatusCode == 403 || resp.StatusCode == 503 {
				cfChallenge = true
			}
		}
	}

	if cfChallenge {
		if resp.Meta == nil {
			resp.Meta = make(map[string]any)
		}
		resp.Meta["cloudflare_challenge"] = true
		m.logger.Warn("Cloudflare challenge detected",
			"url", resp.Request.URLString(),
			"status", resp.StatusCode,
		)
	}

	return resp
}

// DefaultRequestPipeline creates a pipeline with all built-in middlewares.
func DefaultRequestPipeline(logger *slog.Logger) *RequestPipeline {
	rp := NewRequestPipeline(logger)
	rp.Use(NewHeaderRotationMiddleware())
	rp.Use(NewRequestFingerprintMiddleware())
	rp.Use(NewCaptchaDetectionMiddleware(logger))
	rp.Use(NewCloudflareDetectionMiddleware(logger))
	return rp
}

// Ensure all middlewares implement the interface.
var (
	_ RequestMiddleware = (*HeaderRotationMiddleware)(nil)
	_ RequestMiddleware = (*RetryMiddleware)(nil)
	_ RequestMiddleware = (*RateLimitMiddleware)(nil)
	_ RequestMiddleware = (*RequestFingerprintMiddleware)(nil)
	_ RequestMiddleware = (*CaptchaDetectionMiddleware)(nil)
	_ RequestMiddleware = (*ProxyRotationMiddleware)(nil)
	_ RequestMiddleware = (*CloudflareDetectionMiddleware)(nil)
)

// MiddlewareInfo holds summary information about a middleware.
type MiddlewareInfo struct {
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

// List returns info about all registered middlewares.
func (rp *RequestPipeline) List() []MiddlewareInfo {
	rp.mu.RLock()
	defer rp.mu.RUnlock()

	infos := make([]MiddlewareInfo, len(rp.middlewares))
	for i, mw := range rp.middlewares {
		infos[i] = MiddlewareInfo{
			Name:     mw.Name(),
			Priority: mw.Priority(),
		}
	}
	return infos
}

// Describe returns a human-readable description of the pipeline.
func (rp *RequestPipeline) Describe() string {
	rp.mu.RLock()
	defer rp.mu.RUnlock()

	var parts []string
	for _, mw := range rp.middlewares {
		parts = append(parts, fmt.Sprintf("%s (priority=%d)", mw.Name(), mw.Priority()))
	}
	return "RequestPipeline[" + strings.Join(parts, " → ") + "]"
}

package fetcher

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// HTTPFetcher implements Fetcher using net/http.
type HTTPFetcher struct {
	client     *http.Client
	cfg        *config.FetcherConfig
	engineCfg  *config.EngineConfig
	proxyCfg   *config.ProxyConfig
	proxyMgr   *ProxyManager
	logger     *slog.Logger
	userAgents []string
	uaIndex    atomic.Int64
}

// NewHTTPFetcher creates a new HTTP fetcher.
func NewHTTPFetcher(cfg *config.Config, logger *slog.Logger) (*HTTPFetcher, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        cfg.Fetcher.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.Fetcher.MaxIdleConns / 2,
		IdleConnTimeout:     cfg.Fetcher.IdleConnTimeout,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.Fetcher.TLSInsecure, // nolint:gosec // Driven by explicit user config
		},
		DisableCompression: true, // We handle decompression ourselves (including brotli)
	}

	var proxyMgr *ProxyManager
	if cfg.Proxy.Enabled && len(cfg.Proxy.URLs) > 0 {
		proxyMgr = NewProxyManager(&cfg.Proxy, logger)
		transport.Proxy = proxyMgr.ProxyFunc()
	}

	redirectPolicy := func(req *http.Request, via []*http.Request) error {
		if !cfg.Fetcher.FollowRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= cfg.Fetcher.MaxRedirects {
			return fmt.Errorf("max redirects (%d) reached", cfg.Fetcher.MaxRedirects)
		}
		return nil
	}

	client := &http.Client{
		Transport:     transport,
		Jar:           jar,
		Timeout:       cfg.Engine.RequestTimeout,
		CheckRedirect: redirectPolicy,
	}

	return &HTTPFetcher{
		client:     client,
		cfg:        &cfg.Fetcher,
		engineCfg:  &cfg.Engine,
		proxyCfg:   &cfg.Proxy,
		proxyMgr:   proxyMgr,
		logger:     logger.With("component", "http_fetcher"),
		userAgents: cfg.Engine.UserAgents,
	}, nil
}

// Fetch executes an HTTP request and returns the response.
func (f *HTTPFetcher) Fetch(ctx context.Context, req *types.Request) (*types.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URLString(), nil)
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: false}
	}

	// Set User-Agent
	ua := f.nextUserAgent()
	httpReq.Header.Set("User-Agent", ua)

	// Accept brotli, gzip, deflate
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	httpReq.Header.Set("Accept-Encoding", "gzip, deflate, br")
	httpReq.Header.Set("Connection", "keep-alive")

	// Apply custom headers from request
	for key, values := range req.Headers {
		for _, v := range values {
			httpReq.Header.Set(key, v)
		}
	}

	// Set body for POST requests
	if len(req.Body) > 0 {
		httpReq.Body = io.NopCloser(&bytesReaderSimple{data: req.Body})
		httpReq.ContentLength = int64(len(req.Body))
	}

	start := time.Now()
	httpResp, err := f.client.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		retryable := isRetryableError(err)
		return nil, &types.FetchError{
			URL:       req.URLString(),
			Err:       err,
			Retryable: retryable,
		}
	}
	defer httpResp.Body.Close()

	// Handle 429 Too Many Requests — respect Retry-After if present
	if httpResp.StatusCode == 429 {
		retryAfter := parseRetryAfter(httpResp.Header.Get("Retry-After"))
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		return nil, &types.FetchError{
			URL:        req.URLString(),
			StatusCode: httpResp.StatusCode,
			Err:        fmt.Errorf("HTTP 429: rate limited (retry after %s): %s", retryAfter, strings.TrimSpace(string(body))),
			Retryable:  true,
			RetryAfter: retryAfter,
		}
	}

	// Retry on 5xx server errors
	if httpResp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1024))
		return nil, &types.FetchError{
			URL:        req.URLString(),
			StatusCode: httpResp.StatusCode,
			Err:        fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, string(body)),
			Retryable:  true,
		}
	}

	// Read body with size limit
	var reader io.Reader = httpResp.Body
	if f.cfg.MaxBodySize > 0 {
		reader = io.LimitReader(reader, f.cfg.MaxBodySize)
	}

	// Decompress if needed (gzip, deflate, brotli)
	reader, err = decompressReader(httpResp, reader)
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: false}
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: true}
	}

	resp := types.NewResponse(req, httpResp, body, duration)

	f.logger.Debug("fetch complete",
		"url", req.URLString(),
		"status", resp.StatusCode,
		"size", len(body),
		"duration", duration,
	)

	return resp, nil
}

// Close releases resources.
func (f *HTTPFetcher) Close() error {
	f.client.CloseIdleConnections()
	return nil
}

// Type returns the fetcher type identifier.
func (f *HTTPFetcher) Type() string {
	return "http"
}

// nextUserAgent returns the next User-Agent in rotation.
func (f *HTTPFetcher) nextUserAgent() string {
	if len(f.userAgents) == 0 {
		return "ScrapeGoat/" + config.Version
	}
	idx := f.uaIndex.Add(1) % int64(len(f.userAgents))
	return f.userAgents[idx]
}

// decompressReader wraps a reader with the appropriate decompressor.
// Handles gzip, deflate, and brotli (br) encodings.
func decompressReader(resp *http.Response, reader io.Reader) (io.Reader, error) {
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		return gzip.NewReader(reader)
	case "deflate":
		return flate.NewReader(reader), nil
	case "br":
		return brotli.NewReader(reader), nil
	default:
		return reader, nil
	}
}

// isRetryableError checks if a network error warrants a retry.
// Covers timeouts, connection resets, unexpected EOF, and connection refused.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// Context cancellation is NOT retryable
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Unexpected EOF mid-stream — retryable
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	// Network-level errors
	if netErr, ok := err.(net.Error); ok {
		if netErr.Timeout() {
			return true
		}
	}
	// Connection reset by peer, connection refused
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, syscall.ECONNRESET) ||
			errors.Is(opErr.Err, syscall.ECONNREFUSED) {
			return true
		}
	}
	return false
}

// parseRetryAfter parses the Retry-After header value.
// Supports both integer seconds and HTTP-date formats.
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 5 * time.Second // default back-off
	}
	// Try seconds integer
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
		if secs > 120 {
			secs = 120 // cap at 2 minutes
		}
		return time.Duration(secs) * time.Second
	}
	// Try HTTP-date
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d < 0 {
			return time.Second
		}
		if d > 2*time.Minute {
			return 2 * time.Minute
		}
		return d
	}
	return 5 * time.Second
}

// bytesReaderSimple is a simple io.Reader for a byte slice.
type bytesReaderSimple struct {
	data []byte
	pos  int
}

func (r *bytesReaderSimple) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// RandomDelay returns a random delay around the base duration (±25%).
func RandomDelay(base time.Duration) time.Duration {
	jitter := float64(base) * 0.25
	return base + time.Duration(rand.Float64()*2*jitter-jitter)
}

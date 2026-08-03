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

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/safety"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
	"github.com/andybalholm/brotli"
)

// HTTPFetcher implements Fetcher using net/http.
type HTTPFetcher struct {
	client     *http.Client
	cfg        *config.FetcherConfig
	engineCfg  *config.EngineConfig
	proxyCfg   *config.ProxyConfig
	proxyMgr   *ProxyManager
	guard      *safety.URLGuard
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

	// Every outbound connection goes through the guard's dialer, which resolves the
	// host, refuses non-public addresses, and then connects to the address it just
	// checked. The transport re-dials on each redirect hop, so this covers redirects
	// too — see internal/safety for why the hostname alone is not enough.
	guard := safety.New(safety.Config{
		AllowedSchemes:        cfg.Safety.AllowedSchemes,
		AllowPrivateAddresses: cfg.Safety.AllowPrivateAddresses,
		AllowedPrivateHosts:   cfg.Safety.AllowedPrivateHosts,
		DialTimeout:           30 * time.Second,
		KeepAlive:             30 * time.Second,
	})

	transport := &http.Transport{
		DialContext:         guard.DialContext,
		ForceAttemptHTTP2:   true,
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

	// The guard's scheme check runs per hop here, because a 302 to file:// or
	// gopher:// is rejected before a connection is attempted and so would never
	// reach the dialer.
	guardRedirect := guard.CheckRedirect(cfg.Fetcher.MaxRedirects)
	redirectPolicy := func(req *http.Request, via []*http.Request) error {
		if !cfg.Fetcher.FollowRedirects {
			return http.ErrUseLastResponse
		}
		return guardRedirect(req, via)
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
		guard:      guard,
		logger:     logger.With("component", "http_fetcher"),
		userAgents: cfg.Engine.UserAgents,
	}, nil
}

// Fetch executes an HTTP request and returns the response.
func (f *HTTPFetcher) Fetch(ctx context.Context, req *types.Request) (*types.Response, error) {
	// Reject disallowed schemes up front so the caller gets a clear error rather than
	// an obscure transport failure. The address checks happen later, in the dialer,
	// where they cannot be raced by DNS rebinding.
	if err := f.guard.ValidateParsedURL(req.URL); err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: false}
	}

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

	// Read the body under a hard cap on BOTH the compressed stream and the decompressed
	// result. Capping only the compressed stream is not enough: a ~1000:1 gzip bomb slips
	// under a 10 MB compressed limit and expands to ~10 GB inside io.ReadAll.
	maxBody := f.cfg.MaxBodySize

	counted := &countingReader{r: httpResp.Body}
	var compressed io.Reader = counted
	if maxBody > 0 {
		compressed = io.LimitReader(counted, maxBody)
	}

	decompressed, err := decompressReader(httpResp, compressed)
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: false}
	}
	defer func() { _ = decompressed.Close() }()

	// Read one byte past the limit so that hitting it is distinguishable from a body
	// that happens to be exactly max_body_size.
	var plain io.Reader = decompressed
	if maxBody > 0 {
		plain = io.LimitReader(decompressed, maxBody+1)
	}

	body, err := io.ReadAll(plain)
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: true}
	}

	if maxBody > 0 && int64(len(body)) > maxBody {
		return nil, &types.FetchError{
			URL:       req.URLString(),
			Err:       fmt.Errorf("%w (%d bytes)", types.ErrBodyTooLarge, maxBody),
			Retryable: false,
		}
	}

	// Reject bombs that stay under the size cap but expand absurdly. The floor avoids
	// false positives on tiny bodies, where a few hundred bytes of gzip framing makes
	// the ratio meaningless.
	if read := counted.Count(); read >= minRatioCheckBytes {
		if ratio := int64(len(body)) / read; ratio > maxCompressionRatio {
			return nil, &types.FetchError{
				URL:       req.URLString(),
				Err:       fmt.Errorf("%w: %d:1", types.ErrCompressionRatio, ratio),
				Retryable: false,
			}
		}
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

const (
	// maxCompressionRatio is the largest decompressed:compressed ratio we accept.
	// Real-world HTML sits around 5–20:1; anything past 100:1 is a bomb, not a page.
	maxCompressionRatio = 100

	// minRatioCheckBytes is the compressed size below which the ratio check is skipped,
	// since framing overhead dominates on very small bodies.
	minRatioCheckBytes = 1024
)

// countingReader tallies the bytes pulled from the underlying reader, so the caller
// can compare compressed input against decompressed output.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Count returns the number of bytes read so far.
func (c *countingReader) Count() int64 { return c.n }

// decompressReader wraps a reader with the appropriate decompressor.
// Handles gzip, deflate, and brotli (br) encodings. The returned ReadCloser must be
// closed by the caller — gzip and flate both hold resources that leak otherwise.
func decompressReader(resp *http.Response, reader io.Reader) (io.ReadCloser, error) {
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		zr, err := gzip.NewReader(reader)
		if err != nil {
			return nil, err
		}
		return zr, nil
	case "deflate":
		return flate.NewReader(reader), nil
	case "br":
		return io.NopCloser(brotli.NewReader(reader)), nil
	default:
		return io.NopCloser(reader), nil
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
	// Try seconds integer. The value is server-supplied, so it needs clamping at
	// both ends: the HTTP-date branch below already floors at zero, and a bare
	// "Retry-After: -1" produced a negative duration here.
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
		if secs < 0 {
			secs = 0
		}
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

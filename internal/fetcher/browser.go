package fetcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// BrowserFetcher implements Fetcher using a headless browser via Rod.
type BrowserFetcher struct {
	browser    *rod.Browser
	cfg        *config.Config
	stealthCfg *StealthConfig
	logger     *slog.Logger
	proxyMgr   *ProxyManager
	pagePool   chan *rod.Page
	maxPages   int
}

// BrowserOption configures the BrowserFetcher.
type BrowserOption func(*BrowserFetcher)

// WithStealth enables stealth mode with the given configuration.
func WithStealth(cfg *StealthConfig) BrowserOption {
	return func(bf *BrowserFetcher) { bf.stealthCfg = cfg }
}

// WithBrowserProxy sets the proxy manager for browser requests.
func WithBrowserProxy(pm *ProxyManager) BrowserOption {
	return func(bf *BrowserFetcher) { bf.proxyMgr = pm }
}

// WithMaxPages sets the maximum number of concurrent browser pages.
func WithMaxPages(n int) BrowserOption {
	return func(bf *BrowserFetcher) { bf.maxPages = n }
}

// NewBrowserFetcher creates a new headless browser fetcher.
func NewBrowserFetcher(cfg *config.Config, logger *slog.Logger, opts ...BrowserOption) (*BrowserFetcher, error) {
	bf := &BrowserFetcher{
		cfg:      cfg,
		logger:   logger.With("component", "browser_fetcher"),
		maxPages: cfg.Engine.Concurrency,
	}

	for _, opt := range opts {
		opt(bf)
	}

	// Launch browser
	launchURL, err := bf.launchBrowser()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	// Connect to browser
	browser := rod.New().ControlURL(launchURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}

	bf.browser = browser
	bf.pagePool = make(chan *rod.Page, bf.maxPages)

	bf.logger.Info("browser fetcher ready",
		"max_pages", bf.maxPages,
		"stealth", bf.stealthCfg != nil,
	)

	return bf, nil
}

// launchBrowser starts a Chromium instance with appropriate flags.
func (bf *BrowserFetcher) launchBrowser() (string, error) {
	l := launcher.New().
		Headless(true).
		Set("disable-gpu").
		Set("disable-dev-shm-usage").
		Set("no-sandbox").
		Set("disable-setuid-sandbox").
		Set("disable-web-security").
		Set("disable-features", "IsolateOrigins,site-per-process").
		Set("disable-blink-features", "AutomationControlled")

	// Set proxy if available
	if bf.proxyMgr != nil {
		proxyURL := bf.proxyMgr.Next()
		if proxyURL != nil {
			l = l.Proxy(proxyURL.String())
		}
	}

	// Stealth: additional launch flags
	if bf.stealthCfg != nil {
		if bf.stealthCfg.UserDataDir != "" {
			l = l.UserDataDir(bf.stealthCfg.UserDataDir)
		}
		if bf.stealthCfg.WindowSize != "" {
			l = l.Set("window-size", bf.stealthCfg.WindowSize)
		}
	}

	return l.Launch()
}

// Fetch navigates to a URL and returns the rendered page content.
func (bf *BrowserFetcher) Fetch(ctx context.Context, req *types.Request) (*types.Response, error) {
	start := time.Now()

	page, err := bf.getPage()
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: true}
	}
	defer bf.putPage(page)

	// Apply stealth patches if configured
	if bf.stealthCfg != nil {
		page, err = stealth.Page(bf.browser)
		if err != nil {
			return nil, &types.FetchError{URL: req.URLString(), Err: fmt.Errorf("stealth page: %w", err), Retryable: true}
		}
	}

	// Set custom User-Agent if provided
	if ua := req.Headers.Get("User-Agent"); ua != "" {
		err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
			UserAgent: ua,
		})
		if err != nil {
			bf.logger.Warn("failed to set user agent", "error", err)
		}
	}

	// Set custom headers
	if len(req.Headers) > 0 {
		headers := make([]string, 0, len(req.Headers)*2)
		for k, vals := range req.Headers {
			if k == "User-Agent" {
				continue // Already handled
			}
			for _, v := range vals {
				headers = append(headers, k, v)
			}
		}
		if len(headers) > 0 {
			_, _ = page.SetExtraHeaders(headers)
		}
	}

	// Set cookies from request meta
	if cookies, ok := req.Meta["cookies"]; ok {
		if cookieList, ok := cookies.([]*proto.NetworkCookieParam); ok {
			err := page.SetCookies(cookieList)
			if err != nil {
				bf.logger.Warn("failed to set cookies", "error", err)
			}
		}
	}

	// Navigate with timeout
	timeout := bf.cfg.Engine.RequestTimeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}

	err = page.Timeout(timeout).Navigate(req.URLString())
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: true}
	}

	// Wait for page load
	err = page.Timeout(timeout).WaitStable(300 * time.Millisecond)
	if err != nil {
		bf.logger.Warn("page stability timeout, continuing", "url", req.URLString(), "error", err)
	}

	// Execute any custom JavaScript actions
	if jsCode, ok := req.Meta["js_eval"]; ok {
		if js, ok := jsCode.(string); ok && js != "" {
			_, err := page.Eval(js)
			if err != nil {
				bf.logger.Warn("js eval error", "url", req.URLString(), "error", err)
			}
			// Wait for any dynamic content after JS execution
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Wait for selector if specified
	if selector, ok := req.Meta["wait_selector"]; ok {
		if sel, ok := selector.(string); ok && sel != "" {
			err := page.Timeout(10 * time.Second).MustElement(sel).WaitVisible()
			if err != nil {
				bf.logger.Warn("wait selector timeout", "selector", sel, "error", err)
			}
		}
	}

	// Get page content
	html, err := page.HTML()
	if err != nil {
		return nil, &types.FetchError{URL: req.URLString(), Err: err, Retryable: true}
	}

	// Get final URL (after any redirects)
	info, err := page.Info()
	finalURL := req.URLString()
	if err == nil && info != nil {
		finalURL = info.URL
	}

	// Get status code from the page's network events
	statusCode := 200 // Default — Rod doesn't easily expose status codes

	duration := time.Since(start)
	resp := types.NewBrowserResponse(req, statusCode, []byte(html), finalURL, duration)

	// Extract cookies and store in response meta
	pageCookies, _ := page.Cookies(nil)
	if len(pageCookies) > 0 {
		resp.Meta["cookies"] = pageCookies
	}

	bf.logger.Debug("browser fetch complete",
		"url", req.URLString(),
		"final_url", finalURL,
		"size", len(html),
		"duration", duration,
	)

	return resp, nil
}

// Close shuts down the browser and releases resources.
func (bf *BrowserFetcher) Close() error {
	close(bf.pagePool)
	for page := range bf.pagePool {
		_ = page.Close()
	}
	if bf.browser != nil {
		return bf.browser.Close()
	}
	return nil
}

// Type returns the fetcher type identifier.
func (bf *BrowserFetcher) Type() string {
	return "browser"
}

// getPage retrieves a page from the pool or creates a new one.
func (bf *BrowserFetcher) getPage() (*rod.Page, error) {
	select {
	case page := <-bf.pagePool:
		return page, nil
	default:
		return bf.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	}
}

// putPage returns a page to the pool.
func (bf *BrowserFetcher) putPage(page *rod.Page) {
	// Navigate to blank to free memory from the last page
	_ = page.Navigate("about:blank")

	select {
	case bf.pagePool <- page:
	default:
		_ = page.Close() // Pool full, close the page
	}
}

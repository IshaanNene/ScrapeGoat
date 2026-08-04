package types

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Priority levels for request scheduling.
const (
	PriorityHighest = 0
	PriorityHigh    = 1
	PriorityNormal  = 2
	PriorityLow     = 3
	PriorityLowest  = 4
)

// Request represents an HTTP request to be fetched by the crawler.
type Request struct {
	// URL is the target URL to fetch.
	URL *url.URL

	// Method is the HTTP method (GET, POST, etc.). Defaults to GET.
	Method string

	// Headers are custom HTTP headers to send with the request.
	Headers http.Header

	// Body is the request body for POST/PUT requests.
	Body []byte

	// Depth is the crawl depth from the seed URL.
	Depth int

	// Priority controls scheduling order (lower = higher priority).
	Priority int

	// MaxRetries is the maximum number of retries for this request.
	MaxRetries int

	// RetryCount tracks the current retry attempt.
	RetryCount int

	// Timeout overrides the global request timeout for this request.
	Timeout time.Duration

	// Meta stores arbitrary metadata attached to this request.
	Meta map[string]any

	// Tag categorizes this request (e.g., "listing", "detail", "pagination").
	Tag string

	// FetcherType specifies which fetcher to use: "http" or "browser".
	FetcherType string

	// Callbacks are the names of callback functions to invoke on response.
	Callbacks []string

	// ParentURL tracks which page this request was discovered on.
	ParentURL string

	// CreatedAt is when this request was created.
	CreatedAt time.Time

	// ID is a unique identifier for this request.
	ID string
}

// NewRequest creates a new Request with sensible defaults.
func NewRequest(rawURL string) (*Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}

	return &Request{
		URL:         u,
		Method:      http.MethodGet,
		Headers:     make(http.Header),
		Priority:    PriorityNormal,
		MaxRetries:  3,
		FetcherType: "http",
		Meta:        make(map[string]any),
		CreatedAt:   time.Now(),
		ID:          fmt.Sprintf("%s-%d", u.String(), time.Now().UnixNano()),
	}, nil
}

// URLString returns the string representation of the request URL.
func (r *Request) URLString() string {
	if r.URL == nil {
		return ""
	}
	return r.URL.String()
}

// Domain returns the hostname of the request URL.
func (r *Request) Domain() string {
	if r.URL == nil {
		return ""
	}
	return r.URL.Hostname()
}

// Clone creates a deep copy of the request.
func (r *Request) Clone() *Request {
	clone := *r
	if r.URL != nil {
		u := *r.URL
		clone.URL = &u
	}
	clone.Headers = r.Headers.Clone()
	clone.Meta = make(map[string]any, len(r.Meta))
	for k, v := range r.Meta {
		clone.Meta[k] = v
	}
	clone.Body = append([]byte(nil), r.Body...)
	clone.Callbacks = append([]string(nil), r.Callbacks...)
	return &clone
}

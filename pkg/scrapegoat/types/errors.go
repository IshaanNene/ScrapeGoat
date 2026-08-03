package types

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for common failure modes.
var (
	ErrTimeout        = errors.New("request timed out")
	ErrMaxRetries     = errors.New("max retries exceeded")
	ErrBlocked        = errors.New("blocked by robots.txt")
	ErrMaxDepth       = errors.New("max depth exceeded")
	ErrDuplicate      = errors.New("duplicate URL")
	ErrEmptyResponse  = errors.New("empty response body")
	ErrInvalidURL     = errors.New("invalid URL")
	ErrCrawlStopped   = errors.New("crawl has been stopped")
	ErrNoFetcher      = errors.New("no fetcher available for request")
	ErrProxyExhausted = errors.New("all proxies exhausted")

	// ErrBodyTooLarge is returned when a response body exceeds fetcher.max_body_size.
	// It is reported rather than silently truncating, because a truncated document
	// parses as valid-but-wrong HTML and produces confusing downstream failures.
	ErrBodyTooLarge = errors.New("response body exceeds max_body_size")

	// ErrCompressionRatio is returned when a compressed response expands by more than
	// the permitted ratio — the signature of a decompression bomb.
	ErrCompressionRatio = errors.New("response compression ratio exceeds limit")
)

// FetchError wraps errors that occur during fetching.
type FetchError struct {
	URL        string
	StatusCode int
	Err        error
	Retryable  bool
	RetryAfter time.Duration // populated from Retry-After header on HTTP 429
}

func (e *FetchError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("fetch error for %s (status %d): %v", e.URL, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("fetch error for %s: %v", e.URL, e.Err)
}

func (e *FetchError) Unwrap() error { return e.Err }

func (e *FetchError) IsRetryable() bool { return e.Retryable }

// ParseError wraps errors that occur during parsing.
type ParseError struct {
	URL      string
	Selector string
	Err      error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error for %s (selector=%q): %v", e.URL, e.Selector, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// StorageError wraps errors that occur during storage/export.
type StorageError struct {
	Backend string
	Err     error
}

func (e *StorageError) Error() string {
	return fmt.Sprintf("storage error (%s): %v", e.Backend, e.Err)
}

func (e *StorageError) Unwrap() error { return e.Err }

// PipelineError wraps errors that occur in the processing pipeline.
type PipelineError struct {
	Stage string
	Item  *Item
	Err   error
}

func (e *PipelineError) Error() string {
	return fmt.Sprintf("pipeline error at stage %q: %v", e.Stage, e.Err)
}

func (e *PipelineError) Unwrap() error { return e.Err }

package fetcher

import (
	"context"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Fetcher is the interface for all request fetcher implementations.
type Fetcher interface {
	// Fetch retrieves the content at the given request's URL.
	Fetch(ctx context.Context, req *types.Request) (*types.Response, error)

	// Close releases any resources held by the fetcher.
	Close() error

	// Type returns the fetcher type identifier.
	Type() string
}

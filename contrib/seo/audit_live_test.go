package seo

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

// TestLiveSEOAudit moved here from the root integration suite when this package
// moved to contrib: a contrib package carries its own tests, so the core suite
// does not depend on contrib to stay green.
func TestLiveSEOAudit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live network test")
	}

	cfg := testutil.LoopbackConfig()
	f, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, err := types.NewRequest("https://quotes.toscrape.com")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := f.Fetch(context.Background(), req)
	if err != nil {
		t.Skipf("no network: %v", err)
	}

	result, err := NewMetaAuditor(testLogger).Audit(resp)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	t.Logf("SEO score: %d/100, %d issues", result.Score, len(result.Issues))
}

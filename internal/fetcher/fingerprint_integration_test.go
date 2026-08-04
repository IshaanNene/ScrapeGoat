package fetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// TestFingerprintProfileOwnsTheIdentity checks that a configured profile supplies
// a coherent identity end to end, rather than a browser TLS handshake paired with
// Go's generic headers — which is a louder automation signal than no
// fingerprinting at all.
func TestFingerprintProfileOwnsTheIdentity(t *testing.T) {
	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer ts.Close()

	cfg := testutil.LoopbackConfig()
	cfg.Fetcher.Fingerprint = "chrome"
	// The test server is plain HTTP, so the uTLS path is not exercised here; this
	// covers the header half of the identity. The TLS half is asserted directly
	// against captured ClientHello bytes in internal/fetcher/fingerprint.
	f, err := NewHTTPFetcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, err := types.NewRequest(ts.URL)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := f.Fetch(context.Background(), req); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	ua := got.Get("User-Agent")
	if !strings.Contains(ua, "Chrome/") {
		t.Errorf("User-Agent = %q, want a Chrome identity", ua)
	}
	if got.Get("sec-ch-ua") == "" {
		t.Error("Chromium profile sent no Client Hints; real Chromium always does")
	}
	// The profile's Accept must survive rather than being replaced by the
	// fetcher's generic one.
	if accept := got.Get("Accept"); !strings.Contains(accept, "image/avif") {
		t.Errorf("Accept = %q; the generic header overwrote the profile's", accept)
	}
}

func TestNoFingerprintKeepsUserAgentRotation(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Fetcher.Fingerprint = "" // default
	cfg.Engine.UserAgents = []string{"Bot/1.0", "Bot/2.0"}

	f, err := NewHTTPFetcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	if f.profile != nil {
		t.Error("a profile was configured when none was requested")
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		seen[f.nextUserAgent()] = true
	}
	if len(seen) != 2 {
		t.Errorf("rotation produced %d agents, want 2", len(seen))
	}
}

func TestUnknownFingerprintIsRejected(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Fetcher.Fingerprint = "netscape-navigator"

	// A typo must fail loudly. Silently falling back to Go's fingerprint would
	// mean an operator believing they are fingerprinted when they are not.
	if _, err := NewHTTPFetcher(cfg, testLogger()); err == nil {
		t.Error("an unknown fingerprint profile should be an error")
	}
}

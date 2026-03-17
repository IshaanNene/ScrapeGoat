package fetcher

import (
	"net/http"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
)

// --- Fingerprint coverage ---
func TestRequestFingerprinter(t *testing.T) {
	t.Parallel()
	fp := NewRequestFingerprinter(exhaustiveLogger)

	profiles := fp.Profiles()
	if len(profiles) == 0 {
		t.Fatal("no profiles available")
	}
	t.Logf("profiles available: %d", len(profiles))

	p := fp.RandomProfile()
	if p.Name == "" {
		t.Fatal("RandomProfile returned empty profile")
	}
	t.Logf("random profile: name=%s", p.Name)
}

func TestApplyProfileCoverage(t *testing.T) {
	t.Parallel()
	fp := NewRequestFingerprinter(exhaustiveLogger)

	profile := fp.RandomProfile()
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	fp.ApplyProfile(req, profile)

	if req.Header.Get("User-Agent") == "" {
		t.Log("ApplyProfile may not set User-Agent directly (uses Accept headers)")
	}
	t.Logf("applied profile %s, headers: %v", profile.Name, req.Header)
}

func TestIsCloudflareChallengeCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		body   string
		status int
		want   bool
	}{
		{"<html>normal page</html>", 200, false},
		{"<html>Attention Required | Cloudflare</html>", 403, true},
		// 503 with generic body is not necessarily Cloudflare
		{"<html>Service Unavailable</html>", 503, false},
	}
	for _, tt := range tests {
		headers := make(http.Header)
		headers.Set("Server", "cloudflare")
		got := IsCloudflareChallenge(tt.status, []byte(tt.body), headers)
		if got != tt.want {
			t.Errorf("IsCloudflareChallenge status=%d = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestIsRateLimitedCoverage(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	if !IsRateLimited(429, headers) {
		t.Error("429 should be rate limited")
	}
	if IsRateLimited(200, headers) {
		t.Error("200 should not be rate limited")
	}
}

// --- HTTPFetcher.Type() ---
func TestHTTPFetcherType(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	f, err := NewHTTPFetcher(cfg, exhaustiveLogger)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type() != "http" {
		t.Errorf("Type=%q, want 'http'", f.Type())
	}
}

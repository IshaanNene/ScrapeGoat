package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ssrfTargets are the addresses an MCP tool must never reach. Tool arguments come
// from a model, and a model that just read an attacker-controlled page can be
// talked into naming any URL — "the caller wouldn't ask for that" is not an
// available assumption here.
//
// Hostnames that depend on the environment's DNS (metadata.google.internal and
// friends) are deliberately absent: they make the test pass or fail for reasons
// that have nothing to do with the guard.
var ssrfTargets = []struct {
	name string
	url  string
}{
	{"aws metadata", "http://169.254.169.254/latest/meta-data/iam/security-credentials/"},
	{"link-local", "http://169.254.1.1/"},
	{"loopback ip", "http://127.0.0.1:6379/"},
	{"loopback name", "http://localhost:5432/"},
	{"private range", "http://10.0.0.1/admin"},
	{"ipv6 loopback", "http://[::1]:8080/"},
	{"file scheme", "file:///etc/passwd"},
	{"gopher scheme", "gopher://127.0.0.1:6379/_INFO"},
	{"not a url", "garbage"},
}

// blockedFor reports whether err is the guard refusing, rather than some unrelated
// failure that happens to look like success at rejecting.
func blockedFor(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "url rejected") ||
		strings.Contains(msg, "blocked non-public address") ||
		strings.Contains(msg, "url scheme not allowed")
}

// TestSingleURLToolsRejectSSRFTargets covers the tools that surface a fetch error
// directly to the caller.
func TestSingleURLToolsRejectSSRFTargets(t *testing.T) {
	server := NewServer(testLogger(), "")

	for _, tool := range []string{"scrapegoat_extract", "scrapegoat_sitemap"} {
		for _, tc := range ssrfTargets {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				args, err := json.Marshal(map[string]any{"url": tc.url})
				if err != nil {
					t.Fatalf("marshal args: %v", err)
				}

				res, err := server.tools.Execute(context.Background(), tool, args)
				if err == nil {
					t.Fatalf("%s accepted %s and returned %+v", tool, tc.url, res)
				}
				if !blockedFor(err) {
					t.Fatalf("%s failed on %s for the wrong reason: %v", tool, tc.url, err)
				}
			})
		}
	}
}

// TestCrawlToolLeaksNothingFromSSRFTargets covers scrapegoat_crawl, which reports
// per-page failures in its result payload rather than as a call error. The property
// that matters is not that the call errors — it is that no bytes come back.
func TestCrawlToolLeaksNothingFromSSRFTargets(t *testing.T) {
	server := NewServer(testLogger(), "")

	for _, tc := range ssrfTargets {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{
				"url":         tc.url,
				"max_pages":   1,
				"max_depth":   1,
				"concurrency": 1,
			})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}

			res, err := server.tools.Execute(context.Background(), "scrapegoat_crawl", args)
			if err != nil {
				if !blockedFor(err) {
					t.Fatalf("crawl failed on %s for the wrong reason: %v", tc.url, err)
				}
				return // rejected at argument validation, which is the ideal outcome
			}

			if len(res.Content) == 0 {
				t.Fatal("crawl returned no content block at all")
			}
			var payload struct {
				Bytes     int   `json:"bytes"`
				Items     int   `json:"items"`
				CrawlData []any `json:"crawl_data"`
			}
			if err := json.Unmarshal([]byte(res.Content[0].Text), &payload); err != nil {
				t.Fatalf("unmarshal crawl result: %v", err)
			}

			if payload.Bytes != 0 || payload.Items != 0 || len(payload.CrawlData) != 0 {
				t.Fatalf("crawl of %s returned data: bytes=%d items=%d pages=%d",
					tc.url, payload.Bytes, payload.Items, len(payload.CrawlData))
			}
		})
	}
}

func TestBatchToolRejectsOneBadURL(t *testing.T) {
	server := NewServer(testLogger(), "")

	args, err := json.Marshal(map[string]any{
		"urls": []string{
			"https://example.com/fine",
			"file:///etc/passwd",
		},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	// One bad URL fails the whole call rather than being silently skipped: a batch
	// that quietly drops entries hides the attack from the operator.
	if _, err := server.tools.Execute(context.Background(), "scrapegoat_batch", args); !blockedFor(err) {
		t.Fatalf("batch should have rejected the file:// URL, got %v", err)
	}
}

func TestToolsAcceptOrdinaryURLs(t *testing.T) {
	server := NewServer(testLogger(), "")

	// Validation must not reject a normal public URL. This does not assert the crawl
	// succeeds — there may be no network here — only that the guard is not the thing
	// that stopped it.
	args, err := json.Marshal(map[string]any{"url": "https://example.com/", "max_pages": 1})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	if _, err := server.tools.Execute(context.Background(), "scrapegoat_crawl", args); blockedFor(err) {
		t.Fatalf("guard rejected an ordinary public URL: %v", err)
	}
}

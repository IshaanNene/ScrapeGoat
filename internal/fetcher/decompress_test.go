package fetcher

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// gzipOf compresses n copies of a single byte — the cheapest way to build a body
// with an extreme compression ratio.
func gzipOf(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(bytes.Repeat([]byte("A"), n)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// serveGzip returns a server that hands back the given pre-compressed payload.
func serveGzip(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func fetchFrom(t *testing.T, url string, maxBody int64) (*types.Response, error) {
	t.Helper()
	cfg := testConfig()
	cfg.Fetcher.MaxBodySize = maxBody
	f, err := NewHTTPFetcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	req, err := types.NewRequest(url)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return f.Fetch(context.Background(), req)
}

// TestDecompressionBombRejected is the regression test for the size limit being
// applied to the compressed stream only. Before the fix, a small gzip payload that
// expands past max_body_size was read into memory in full.
func TestDecompressionBombRejected(t *testing.T) {
	const maxBody = 64 * 1024

	// ~8 MB of 'A' compresses to a few KB: comfortably under maxBody compressed,
	// 128x over it decompressed.
	payload := gzipOf(t, 8*1024*1024)
	if int64(len(payload)) >= maxBody {
		t.Fatalf("test payload is %d compressed bytes, needs to be under the %d cap "+
			"or it proves nothing", len(payload), maxBody)
	}

	ts := serveGzip(t, payload)
	resp, err := fetchFrom(t, ts.URL, maxBody)

	if err == nil {
		t.Fatalf("expected the bomb to be rejected, got a %d byte body", len(resp.Body))
	}
	if !errors.Is(err, types.ErrBodyTooLarge) && !errors.Is(err, types.ErrCompressionRatio) {
		t.Fatalf("expected ErrBodyTooLarge or ErrCompressionRatio, got %v", err)
	}

	var fe *types.FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("expected a *types.FetchError, got %T", err)
	}
	if fe.Retryable {
		t.Error("a decompression bomb is not retryable — retrying just re-downloads it")
	}
}

// TestCompressionRatioRejected covers the bomb that stays under the size cap but
// still expands absurdly, which the size check alone would let through.
func TestCompressionRatioRejected(t *testing.T) {
	const maxBody = 16 * 1024 * 1024

	// 4 MB of 'A' is well under maxBody decompressed, but compresses ~4000:1.
	payload := gzipOf(t, 4*1024*1024)
	if len(payload) < minRatioCheckBytes {
		t.Skipf("payload compressed to %d bytes, below the %d ratio-check floor",
			len(payload), minRatioCheckBytes)
	}

	ts := serveGzip(t, payload)
	_, err := fetchFrom(t, ts.URL, maxBody)

	if !errors.Is(err, types.ErrCompressionRatio) {
		t.Fatalf("expected ErrCompressionRatio, got %v", err)
	}
}

// TestOrdinaryGzipStillWorks guards against the limits being so tight that normal
// compressed pages are rejected.
func TestOrdinaryGzipStillWorks(t *testing.T) {
	const html = `<html><body><h1>Hello</h1><p>Some ordinary prose that gzip will ` +
		`compress at a perfectly unremarkable ratio, the way a real page does.</p></body></html>`

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(html)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	ts := serveGzip(t, buf.Bytes())
	resp, err := fetchFrom(t, ts.URL, 1024*1024)
	if err != nil {
		t.Fatalf("ordinary gzip page was rejected: %v", err)
	}
	if string(resp.Body) != html {
		t.Errorf("body round-trip mismatch:\n got %q\nwant %q", resp.Body, html)
	}
}

// TestBodyExactlyAtLimit pins the boundary: max_body_size bytes is allowed,
// one byte more is not.
func TestBodyExactlyAtLimit(t *testing.T) {
	const maxBody = 4096

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxBody))
	}))
	t.Cleanup(ts.Close)

	resp, err := fetchFrom(t, ts.URL, maxBody)
	if err != nil {
		t.Fatalf("a body of exactly max_body_size should be accepted, got %v", err)
	}
	if len(resp.Body) != maxBody {
		t.Errorf("got %d bytes, want %d", len(resp.Body), maxBody)
	}
}

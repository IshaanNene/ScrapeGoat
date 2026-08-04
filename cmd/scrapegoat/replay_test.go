package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/fetchlog"
)

// site serves a small linked set of pages and counts what it was asked for.
func site(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()

	pages := map[string]string{
		"/": `<html><head><title>Index</title></head><body>
			<h1>Index</h1><p>The root page has enough prose on it to look like content.</p>
			<a href="/one">one</a> <a href="/two">two</a></body></html>`,
		"/one": `<html><head><title>One</title></head><body>
			<h1>One</h1><p>The first page, with a paragraph long enough to be extracted.</p>
			<a href="/two">two</a></body></html>`,
		"/two": `<html><head><title>Two</title></head><body>
			<h1>Two</h1><p>The second page, likewise carrying real text rather than links.</p>
			</body></html>`,
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)

		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
			return
		}
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
}

// loopbackConfigFile writes a config that permits fetching 127.0.0.1, which the
// default safety policy refuses. The refusal is the SSRF guard working; a test
// driving a local server opts out explicitly rather than the default being
// weakened to suit it.
func loopbackConfigFile(t *testing.T, outputPath string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf(`engine:
  concurrency: 2
  max_depth: 2
  politeness_delay: 0s
  respect_robots_txt: true
safety:
  allow_private_addresses: true
storage:
  type: jsonl
  output_path: %s
metrics:
  enabled: false
`, outputPath)

	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// resetGlobals restores the cobra-bound package variables the commands read, so
// one test's flags do not leak into the next.
func resetGlobals(t *testing.T) {
	t.Helper()
	prev := struct {
		cfgFile, outputPath, outputType, recordDir string
		replayOutput, replayFormat, replayConfig   string
		verbose, resumeCrawl, verifyJSON           bool
		depth, concurrent, maxRequests, maxRetries int
		delay, userAgent, allowedDomains           string
	}{
		cfgFile, outputPath, outputType, recordDir,
		replayOutput, replayFormat, replayConfig,
		verbose, resumeCrawl, verifyJSON,
		depth, concurrent, maxRequests, maxRetries,
		delay, userAgent, allowedDomains,
	}

	t.Cleanup(func() {
		cfgFile, outputPath, outputType, recordDir = prev.cfgFile, prev.outputPath, prev.outputType, prev.recordDir
		replayOutput, replayFormat, replayConfig = prev.replayOutput, prev.replayFormat, prev.replayConfig
		verbose, resumeCrawl, verifyJSON = prev.verbose, prev.resumeCrawl, prev.verifyJSON
		depth, concurrent, maxRequests, maxRetries = prev.depth, prev.concurrent, prev.maxRequests, prev.maxRetries
		delay, userAgent, allowedDomains = prev.delay, prev.userAgent, prev.allowedDomains
	})

	// The command defaults, since a test does not go through cobra's flag parsing.
	depth, concurrent, maxRequests, maxRetries = 0, 0, 0, -1
	delay, userAgent, allowedDomains = "", "", ""
	verbose, resumeCrawl, verifyJSON = false, false, false
	replayConfig = ""
}

// The Tier 1 claim, end to end through the CLI: crawl --record, then replay, and
// the two produce the same output bytes without the replay touching the network.
func TestCrawlRecordThenReplayProducesIdenticalOutput(t *testing.T) {
	resetGlobals(t)

	var hits int32
	srv := site(t, &hits)
	defer srv.Close()

	work := t.TempDir()
	logDir := filepath.Join(work, "log")
	liveOut := filepath.Join(work, "live")
	replayOut := filepath.Join(work, "replay")

	// --- record ---
	cfgFile = loopbackConfigFile(t, liveOut)
	recordDir = logDir
	outputPath, outputType = "", ""

	if err := runCrawl(nil, []string{srv.URL + "/"}); err != nil {
		t.Fatalf("recorded crawl: %v", err)
	}

	recorded := atomic.LoadInt32(&hits)
	if recorded == 0 {
		t.Fatal("the crawl never reached the server")
	}
	if _, err := os.Stat(filepath.Join(liveOut, "results.jsonl")); err != nil {
		t.Fatalf("the live crawl wrote no output: %v", err)
	}

	// The manifest must describe a finished crawl.
	manifest, err := fetchlog.ReadManifest(logDir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if !manifest.Complete() {
		t.Error("manifest does not record the crawl as finished")
	}
	if len(manifest.Seeds) != 1 || manifest.Seeds[0] != srv.URL+"/" {
		t.Errorf("manifest seeds = %v", manifest.Seeds)
	}
	if manifest.ConfigHash == "" {
		t.Error("manifest carries no config hash")
	}
	if manifest.Entries == 0 {
		t.Error("manifest claims zero entries after a crawl that fetched pages")
	}

	// --- replay ---
	recordDir = ""
	replayOutput, replayFormat = replayOut, "jsonl"

	if err := runReplay(nil, []string{logDir}); err != nil {
		t.Fatalf("replay: %v", err)
	}

	if got := atomic.LoadInt32(&hits); got != recorded {
		t.Errorf("the replay made %d requests to the server; it should have made none", got-recorded)
	}

	liveFile := filepath.Join(liveOut, "results.jsonl")
	replayFile := filepath.Join(replayOut, "results.jsonl")
	live, replayed := sha256File(t, liveFile), sha256File(t, replayFile)
	if live != replayed {
		liveBody, _ := os.ReadFile(liveFile)
		replayBody, _ := os.ReadFile(replayFile)
		t.Errorf("replay output differs from the recorded crawl\n live (%s):\n%s\n replay (%s):\n%s",
			live[:12], liveBody, replayed[:12], replayBody)
	}

	// --- and replaying twice must agree with itself ---
	second := filepath.Join(work, "replay2")
	replayOutput = second
	if err := runReplay(nil, []string{logDir}); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if sha256File(t, filepath.Join(second, "results.jsonl")) != replayed {
		t.Error("two replays of the same log produced different output")
	}
}

func TestVerifyAcceptsAnIntactLog(t *testing.T) {
	resetGlobals(t)

	var hits int32
	srv := site(t, &hits)
	defer srv.Close()

	work := t.TempDir()
	logDir := filepath.Join(work, "log")

	cfgFile = loopbackConfigFile(t, filepath.Join(work, "out"))
	recordDir = logDir

	if err := runCrawl(nil, []string{srv.URL + "/"}); err != nil {
		t.Fatalf("recorded crawl: %v", err)
	}

	if err := runVerify(nil, []string{logDir}); err != nil {
		t.Fatalf("verify rejected an intact log: %v", err)
	}
}

// Verification has to catch tampering, or a published dataset proves nothing.
func TestStoreVerificationCatchesTamperedLog(t *testing.T) {
	resetGlobals(t)

	var hits int32
	srv := site(t, &hits)
	defer srv.Close()

	work := t.TempDir()
	logDir := filepath.Join(work, "log")

	cfgFile = loopbackConfigFile(t, filepath.Join(work, "out"))
	recordDir = logDir

	if err := runCrawl(nil, []string{srv.URL + "/"}); err != nil {
		t.Fatalf("recorded crawl: %v", err)
	}

	entries, err := fetchlog.ReadLog(logDir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var digest string
	for _, e := range entries {
		if e.Digest != "" {
			digest = e.Digest
			break
		}
	}
	if digest == "" {
		t.Fatal("the crawl recorded no bodies")
	}

	object := filepath.Join(logDir, "objects", digest[:2], digest[2:])
	if err := os.WriteFile(object, []byte("<html>substituted after the fact</html>"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	store, err := fetchlog.NewStore(logDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Verify(); err == nil {
		t.Fatal("Verify passed a log whose body had been swapped")
	}

	// And a replay must refuse the altered body rather than serve it.
	player, err := fetchlog.NewPlayer(logDir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	defer player.Close()
	if _, err := player.Store().Get(digest); err == nil {
		t.Error("the store handed back tampered bytes")
	}
}

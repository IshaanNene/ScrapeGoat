package fetchlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// --- a scripted fetcher, so tests can dictate outcomes ---

type scriptedFetcher struct {
	mu    sync.Mutex
	calls int32

	// responses per URL, consumed in order; the last one repeats.
	script map[string][]scriptStep
}

type scriptStep struct {
	body   string
	status int
	err    error
}

func (f *scriptedFetcher) Fetch(_ context.Context, req *types.Request) (*types.Response, error) {
	atomic.AddInt32(&f.calls, 1)

	f.mu.Lock()
	defer f.mu.Unlock()

	steps := f.script[req.URLString()]
	if len(steps) == 0 {
		return nil, &types.FetchError{URL: req.URLString(), Err: errors.New("not scripted")}
	}
	step := steps[0]
	if len(steps) > 1 {
		f.script[req.URLString()] = steps[1:]
	}

	if step.err != nil {
		return nil, step.err
	}

	httpResp := &http.Response{
		StatusCode: step.status,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Request:    &http.Request{URL: req.URL},
	}
	return types.NewResponse(req, httpResp, []byte(step.body), 7*time.Millisecond), nil
}

func (f *scriptedFetcher) Close() error { return nil }

func mustRequest(t *testing.T, rawURL string) *types.Request {
	t.Helper()
	req, err := types.NewRequest(rawURL)
	if err != nil {
		t.Fatalf("NewRequest(%q): %v", rawURL, err)
	}
	return req
}

// --- record then replay ---

func TestRecordThenReplay(t *testing.T) {
	dir := t.TempDir()
	const url = "https://example.com/page"

	inner := &scriptedFetcher{script: map[string][]scriptStep{
		url: {{body: "<html>recorded</html>", status: 200}},
	}}

	rec, err := NewRecorder(inner, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	live, err := rec.Fetch(context.Background(), mustRequest(t, url))
	if err != nil {
		t.Fatalf("record fetch: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("recorder Close: %v", err)
	}

	player, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	replayed, err := player.Fetch(context.Background(), mustRequest(t, url))
	if err != nil {
		t.Fatalf("replay fetch: %v", err)
	}

	if string(replayed.Body) != string(live.Body) {
		t.Errorf("body diverged:\n live: %q\n replay: %q", live.Body, replayed.Body)
	}
	if replayed.StatusCode != live.StatusCode {
		t.Errorf("status %d on replay, %d live", replayed.StatusCode, live.StatusCode)
	}
	if replayed.Headers.Get("Content-Type") != live.Headers.Get("Content-Type") {
		t.Errorf("Content-Type diverged: %q vs %q",
			replayed.Headers.Get("Content-Type"), live.Headers.Get("Content-Type"))
	}
	if replayed.FinalURL != live.FinalURL {
		t.Errorf("FinalURL diverged: %q vs %q", replayed.FinalURL, live.FinalURL)
	}

	// The replay reports the recorded latency, not the microseconds a disk read
	// took. Anything downstream reasoning about timing must see the crawl.
	if replayed.FetchDuration != live.FetchDuration {
		t.Errorf("replay reported duration %v, recorded %v",
			replayed.FetchDuration, live.FetchDuration)
	}

	// Same for the wall-clock stamp. types.NewResponse fills FetchedAt with
	// time.Now(), which on a replay is the wrong answer: it would date the
	// dataset to whenever someone last replayed it.
	if !replayed.FetchedAt.Equal(live.FetchedAt) {
		t.Errorf("replay stamped FetchedAt %v, recorded %v",
			replayed.FetchedAt, live.FetchedAt)
	}

	// And the replay must not have touched the wrapped fetcher.
	if got := atomic.LoadInt32(&inner.calls); got != 1 {
		t.Errorf("inner fetcher called %d times; replay went to the network", got)
	}
}

// A replay that could fall through to the network stops being a replay the moment
// the recording is incomplete — and the divergence looks like success.
func TestPlayerRefusesUnrecordedURL(t *testing.T) {
	dir := t.TempDir()

	inner := &scriptedFetcher{script: map[string][]scriptStep{
		"https://example.com/a": {{body: "a", status: 200}},
	}}
	rec, err := NewRecorder(inner, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Fetch(context.Background(), mustRequest(t, "https://example.com/a")); err != nil {
		t.Fatalf("record: %v", err)
	}
	rec.Close()

	player, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	_, err = player.Fetch(context.Background(), mustRequest(t, "https://example.com/never-crawled"))
	if !errors.Is(err, ErrNoRecording) {
		t.Fatalf("got %v, want ErrNoRecording", err)
	}
}

// Failures are part of the crawl. A log holding only successes replays a run that
// never happened: different backoff, different circuit-breaker state.
func TestRecorderRecordsFailures(t *testing.T) {
	dir := t.TempDir()
	const url = "https://example.com/flaky"

	inner := &scriptedFetcher{script: map[string][]scriptStep{
		url: {
			{err: &types.FetchError{URL: url, StatusCode: 503, Err: errors.New("service unavailable"), Retryable: true}},
			{err: &types.FetchError{URL: url, StatusCode: 503, Err: errors.New("service unavailable"), Retryable: true}},
			{body: "<html>finally</html>", status: 200},
		},
	}}

	rec, err := NewRecorder(inner, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	for i := 0; i < 3; i++ {
		_, ferr := rec.Fetch(context.Background(), mustRequest(t, url))
		if i < 2 && ferr == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", i)
		}
		if i == 2 && ferr != nil {
			t.Fatalf("attempt 2 failed: %v", ferr)
		}
	}
	rec.Close()

	entries, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("recorded %d entries, want 3 (two failures and a success)", len(entries))
	}
	for i, e := range entries {
		if e.Attempt != i {
			t.Errorf("entry %d has attempt %d", i, e.Attempt)
		}
	}
	if entries[0].Err == "" || entries[0].StatusCode != 503 || !entries[0].Retryable {
		t.Errorf("first failure lost its detail: %+v", entries[0])
	}
	if entries[2].Err != "" || entries[2].Digest == "" {
		t.Errorf("success entry is wrong: %+v", entries[2])
	}

	// Replaying must hand back the same *sequence*, not the same response thrice.
	player, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, ferr := player.Fetch(context.Background(), mustRequest(t, url))
		if ferr == nil {
			t.Fatalf("replay attempt %d succeeded; the recording says it failed", i)
		}
		var fe *types.FetchError
		if !errors.As(ferr, &fe) {
			t.Fatalf("replay error is %T, want *types.FetchError", ferr)
		}
		if fe.StatusCode != 503 || !fe.Retryable {
			t.Errorf("replayed error lost status/retryability: %+v", fe)
		}
	}
	resp, err := player.Fetch(context.Background(), mustRequest(t, url))
	if err != nil {
		t.Fatalf("replay attempt 2: %v", err)
	}
	if string(resp.Body) != "<html>finally</html>" {
		t.Errorf("replayed body %q", resp.Body)
	}
}

// A replay under a different retry policy asks for attempt 2 where the recording
// only has attempt 0. Refusing there would make policy comparison impossible,
// which is one of the reasons the log exists.
func TestPlayerFallsBackToFirstAttempt(t *testing.T) {
	dir := t.TempDir()
	const url = "https://example.com/once"

	inner := &scriptedFetcher{script: map[string][]scriptStep{
		url: {{body: "only recorded once", status: 200}},
	}}
	rec, err := NewRecorder(inner, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Fetch(context.Background(), mustRequest(t, url)); err != nil {
		t.Fatalf("record: %v", err)
	}
	rec.Close()

	player, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	for i := 0; i < 3; i++ {
		resp, err := player.Fetch(context.Background(), mustRequest(t, url))
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if string(resp.Body) != "only recorded once" {
			t.Errorf("replay %d body %q", i, resp.Body)
		}
	}
}

func TestPlayerCoverage(t *testing.T) {
	dir := t.TempDir()

	inner := &scriptedFetcher{script: map[string][]scriptStep{
		"https://example.com/a": {{body: "a", status: 200}},
		"https://example.com/b": {{body: "b", status: 200}},
	}}
	rec, err := NewRecorder(inner, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	for _, u := range []string{"https://example.com/a", "https://example.com/b"} {
		if _, err := rec.Fetch(context.Background(), mustRequest(t, u)); err != nil {
			t.Fatalf("record %s: %v", u, err)
		}
	}
	rec.Close()

	player, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	have, missing := player.Coverage([]string{
		"https://example.com/a",
		"https://example.com/c",
		"https://example.com/b",
	})
	if len(have) != 2 {
		t.Errorf("Coverage says it has %v", have)
	}
	if len(missing) != 1 || missing[0] != "https://example.com/c" {
		t.Errorf("Coverage says it is missing %v, want [.../c]", missing)
	}
}

// Bodies with identical bytes across different URLs must share one object. The
// store is what makes a kept log affordable; if this regresses, nobody notices
// until the disk fills.
func TestRecorderDeduplicatesIdenticalBodies(t *testing.T) {
	dir := t.TempDir()

	inner := &scriptedFetcher{script: map[string][]scriptStep{
		"https://example.com/x": {{body: "<html>same</html>", status: 200}},
		"https://example.com/y": {{body: "<html>same</html>", status: 200}},
		"https://example.com/z": {{body: "<html>different</html>", status: 200}},
	}}
	rec, err := NewRecorder(inner, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	for _, u := range []string{"https://example.com/x", "https://example.com/y", "https://example.com/z"} {
		if _, err := rec.Fetch(context.Background(), mustRequest(t, u)); err != nil {
			t.Fatalf("record %s: %v", u, err)
		}
	}
	st, err := rec.Store().Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	rec.Close()

	if st.Objects != 2 {
		t.Errorf("three fetches of two distinct bodies left %d objects, want 2", st.Objects)
	}

	entries, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if entries[0].Digest != entries[1].Digest {
		t.Error("identical bodies got different addresses")
	}
	if entries[2].Digest == entries[0].Digest {
		t.Error("different bodies collided on one address")
	}
}

// --- end to end, against a real server through the real fetcher ---

// This is the Tier 1 claim: a crawl recorded off the wire and replayed off disk
// produces the same bytes, and the replay opens no sockets. It runs through the
// production HTTP fetcher rather than a stub, because a record/replay pair that
// only agrees with itself proves nothing about the fetcher it wraps.
func TestEndToEndReplayIsByteIdentical(t *testing.T) {
	var hits int32
	pages := map[string]string{
		"/":      `<html><head><title>Index</title></head><body><h1>Index</h1><p>Root page.</p><a href="/one">one</a></body></html>`,
		"/one":   `<html><head><title>One</title></head><body><h1>One</h1><p>First page body.</p></body></html>`,
		"/two":   `<html><head><title>Two</title></head><body><h1>Two</h1><p>Second page body.</p></body></html>`,
		"/gone":  "",
		"/robot": "",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)

		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusNotFound)
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
	defer srv.Close()

	dir := t.TempDir()
	urls := []string{srv.URL + "/", srv.URL + "/one", srv.URL + "/two", srv.URL + "/gone"}

	// --- record ---
	httpFetcher, err := fetcher.NewHTTPFetcher(testutil.LoopbackConfig(), slog.Default())
	if err != nil {
		t.Fatalf("NewHTTPFetcher: %v", err)
	}
	rec, err := NewRecorder(httpFetcher, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	type outcome struct {
		body   string
		status int
		err    bool
	}
	live := make([]outcome, len(urls))
	for i, u := range urls {
		resp, ferr := rec.Fetch(context.Background(), mustRequest(t, u))
		if ferr != nil {
			live[i] = outcome{err: true}
			continue
		}
		live[i] = outcome{body: string(resp.Body), status: resp.StatusCode}
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("recorder Close: %v", err)
	}

	recorded := atomic.LoadInt32(&hits)
	if recorded == 0 {
		t.Fatal("the recording never reached the server")
	}

	// --- replay ---
	player, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	for i, u := range urls {
		resp, ferr := player.Fetch(context.Background(), mustRequest(t, u))
		if ferr != nil {
			if !live[i].err {
				t.Errorf("%s failed on replay but succeeded live: %v", u, ferr)
			}
			continue
		}
		if live[i].err {
			t.Errorf("%s succeeded on replay but failed live", u)
			continue
		}
		if string(resp.Body) != live[i].body {
			t.Errorf("%s: body diverged\n live: %q\n replay: %q", u, live[i].body, resp.Body)
		}
		if resp.StatusCode != live[i].status {
			t.Errorf("%s: status %d on replay, %d live", u, resp.StatusCode, live[i].status)
		}
	}
	if err := player.Close(); err != nil {
		t.Fatalf("player Close: %v", err)
	}

	// The server must not have been touched again. This is the assertion that
	// separates a replay from a re-crawl.
	if got := atomic.LoadInt32(&hits); got != recorded {
		t.Errorf("replay made %d additional requests to the server", got-recorded)
	}

	// The log the replay read from must be intact and self-verifying.
	if err := player.Store().Verify(); err != nil {
		t.Errorf("store failed verification after replay: %v", err)
	}

	// Replaying the same log twice must give the same answer. If it does not, the
	// log is not a record, it is a source of a different run each time.
	second, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("second NewPlayer: %v", err)
	}
	for i, u := range urls {
		resp, ferr := second.Fetch(context.Background(), mustRequest(t, u))
		if (ferr != nil) != live[i].err {
			t.Errorf("%s: second replay disagrees with the first about failure", u)
			continue
		}
		if ferr == nil && string(resp.Body) != live[i].body {
			t.Errorf("%s: second replay produced different bytes", u)
		}
	}
}

// Replay is concurrent because crawls are. Sharing a Player across workers must
// not corrupt attempt accounting or hand back a body from another URL.
func TestPlayerConcurrentReplay(t *testing.T) {
	dir := t.TempDir()

	script := map[string][]scriptStep{}
	urls := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		u := fmt.Sprintf("https://example.com/p%02d", i)
		urls = append(urls, u)
		script[u] = []scriptStep{{body: fmt.Sprintf("body of %s", u), status: 200}}
	}

	rec, err := NewRecorder(&scriptedFetcher{script: script}, dir, nil)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	for _, u := range urls {
		if _, err := rec.Fetch(context.Background(), mustRequest(t, u)); err != nil {
			t.Fatalf("record %s: %v", u, err)
		}
	}
	rec.Close()

	player, err := NewPlayer(dir)
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, u := range urls {
				resp, err := player.Fetch(context.Background(), mustRequest(t, u))
				if err != nil {
					t.Errorf("replay %s: %v", u, err)
					return
				}
				if want := fmt.Sprintf("body of %s", u); string(resp.Body) != want {
					t.Errorf("replay %s returned %q, want %q", u, resp.Body, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

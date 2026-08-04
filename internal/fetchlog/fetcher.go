package fetchlog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/IshaanNene/ScrapeGoat/internal/clock"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Fetcher is the engine's fetcher interface, restated here so this package does
// not import internal/engine and create a cycle. Both Recorder and Player satisfy
// it, which is the point: the engine cannot tell whether it is talking to the
// network or to a log.
type Fetcher interface {
	Fetch(ctx context.Context, req *types.Request) (*types.Response, error)
	Close() error
}

// --- Recording ---

// Recorder wraps a Fetcher and writes every attempt to the log.
//
// It records failures as well as successes. A crawl that hit three 503s before a
// 200 took a different path through backoff and the circuit breaker than one that
// succeeded immediately, so a log holding only the 200 would replay a crawl that
// never happened.
type Recorder struct {
	inner Fetcher
	store *Store
	log   *Log
	clock clock.Clock

	mu        sync.Mutex
	attempts  map[string]int // method+url -> attempts so far
	closeOnce sync.Once
	closeErr  error
}

// NewRecorder wraps inner, writing to dir.
func NewRecorder(inner Fetcher, dir string, clk clock.Clock) (*Recorder, error) {
	store, err := NewStore(dir)
	if err != nil {
		return nil, err
	}
	log, err := OpenLog(dir)
	if err != nil {
		return nil, err
	}

	return &Recorder{
		inner:    inner,
		store:    store,
		log:      log,
		clock:    clock.OrSystem(clk),
		attempts: make(map[string]int),
	}, nil
}

func attemptID(method, rawURL string) string { return method + " " + rawURL }

// nextAttempt returns the attempt number for this request and increments it.
func (r *Recorder) nextAttempt(method, rawURL string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := attemptID(method, rawURL)
	n := r.attempts[k]
	r.attempts[k] = n + 1
	return n
}

// Fetch performs the fetch and records the outcome.
func (r *Recorder) Fetch(ctx context.Context, req *types.Request) (*types.Response, error) {
	attempt := r.nextAttempt(req.Method, req.URLString())
	start := r.clock.Now()

	resp, err := r.inner.Fetch(ctx, req)

	entry := Entry{
		Method:    req.Method,
		URL:       req.URLString(),
		Attempt:   attempt,
		FetchedAt: start,
		Duration:  r.clock.Since(start),

		// The engine consults robots.txt before it ever reaches a fetcher, so a
		// request arriving here was permitted. Recorded explicitly because the
		// question is asked later, and only the moment of fetching knows.
		RobotsAllowed: true,
	}

	if err != nil {
		entry.Err = err.Error()

		var fe *types.FetchError
		if errors.As(err, &fe) {
			entry.StatusCode = fe.StatusCode
			entry.Retryable = fe.Retryable
		}

		if _, aerr := r.log.Append(entry); aerr != nil {
			// A log write failure must not silently drop the record. Surfacing it
			// alongside the fetch error is better than discarding either.
			return nil, fmt.Errorf("%w (and recording it failed: %w)", err, aerr)
		}
		return nil, err
	}

	digest, serr := r.store.Put(resp.Body)
	if serr != nil {
		return nil, fmt.Errorf("fetchlog: store body for %s: %w", req.URLString(), serr)
	}

	entry.Digest = digest
	entry.StatusCode = resp.StatusCode
	entry.Headers = resp.Headers
	entry.FinalURL = resp.FinalURL

	// Take the timing from the response rather than from our own measurement
	// around the call. The two differ by the wrapper's overhead, and what replay
	// has to reproduce is what the live run *reported* downstream — otherwise a
	// replayed response carries numbers a shade off from the recorded ones and
	// "bit-identical" quietly stops being true. Our measurement stands for
	// failures, where there is no response to ask.
	entry.FetchedAt = resp.FetchedAt
	entry.Duration = resp.FetchDuration

	if _, aerr := r.log.Append(entry); aerr != nil {
		return nil, fmt.Errorf("fetchlog: record %s: %w", req.URLString(), aerr)
	}
	return resp, nil
}

// Close closes the log and the wrapped fetcher. Safe to call more than once.
//
// Two owners legitimately close a Recorder: the engine closes the fetchers it was
// given, and the code that opened the log closes it to seal the recording. Both
// are right, so the second call has to be a no-op rather than an error report on
// an otherwise clean shutdown.
func (r *Recorder) Close() error {
	r.closeOnce.Do(func() {
		logErr := r.log.Close()
		innerErr := r.inner.Close()
		if logErr != nil {
			r.closeErr = logErr
			return
		}
		r.closeErr = innerErr
	})
	return r.closeErr
}

// Store exposes the underlying object store.
func (r *Recorder) Store() *Store { return r.store }

// --- Replay ---

// ErrNoRecording is returned when a replay is asked for a URL the log does not
// contain.
var ErrNoRecording = errors.New("fetchlog: no recording for request")

// Player serves responses from a recorded log instead of the network.
//
// It opens no sockets. That is what makes replay both fast and honest: a replay
// that could fall through to the network on a miss would quietly stop being a
// replay the moment the recording was incomplete, and the divergence would look
// like a successful run.
type Player struct {
	store   *Store
	entries map[attemptKey]Entry

	mu       sync.Mutex
	attempts map[string]int

	// strict controls what a missing recording does. Default is to fail.
	strict bool
}

// NewPlayer opens a log for replay.
func NewPlayer(dir string) (*Player, error) {
	store, err := NewStore(dir)
	if err != nil {
		return nil, err
	}

	entries, err := ReadLog(dir)
	if err != nil {
		return nil, err
	}

	byKey := make(map[attemptKey]Entry, len(entries))
	for _, e := range entries {
		byKey[e.key()] = e
	}

	return &Player{
		store:    store,
		entries:  byKey,
		attempts: make(map[string]int),
		strict:   true,
	}, nil
}

// Fetch returns the recorded response for this request and attempt.
func (p *Player) Fetch(_ context.Context, req *types.Request) (*types.Response, error) {
	rawURL := req.URLString()

	p.mu.Lock()
	k := attemptID(req.Method, rawURL)
	attempt := p.attempts[k]
	p.attempts[k] = attempt + 1
	p.mu.Unlock()

	entry, ok := p.entries[attemptKey{method: req.Method, url: rawURL, attempt: attempt}]
	if !ok {
		// Fall back to attempt 0. A replay under a different retry policy will ask
		// for attempt 2 where the recording only has attempt 0, and refusing there
		// would make policy comparison impossible — which is one of the three
		// reasons the log exists.
		entry, ok = p.entries[attemptKey{method: req.Method, url: rawURL, attempt: 0}]
		if !ok {
			if p.strict {
				return nil, fmt.Errorf("%w: %s %s (attempt %d)",
					ErrNoRecording, req.Method, rawURL, attempt)
			}
			return nil, &types.FetchError{
				URL: rawURL, Err: ErrNoRecording, Retryable: false,
			}
		}
	}

	if entry.Err != "" {
		return nil, &types.FetchError{
			URL:        rawURL,
			StatusCode: entry.StatusCode,
			Err:        errors.New(entry.Err),
			Retryable:  entry.Retryable,
		}
	}

	body, err := p.store.Get(entry.Digest)
	if err != nil {
		return nil, fmt.Errorf("fetchlog: replay %s: %w", rawURL, err)
	}

	return p.response(req, entry, body)
}

// response rebuilds a types.Response from a log entry and its body.
func (p *Player) response(req *types.Request, e Entry, body []byte) (*types.Response, error) {
	finalURL := e.FinalURL
	if finalURL == "" {
		finalURL = e.URL
	}
	u, err := url.Parse(finalURL)
	if err != nil {
		return nil, fmt.Errorf("fetchlog: parse recorded url %q: %w", finalURL, err)
	}

	headers := e.Headers
	if headers == nil {
		headers = http.Header{}
	}

	httpResp := &http.Response{
		StatusCode: e.StatusCode,
		Header:     headers,
		Request:    &http.Request{URL: u},
	}

	// Duration comes from the recording rather than from measuring the replay.
	// A replayed crawl that reported microsecond fetches would misrepresent the
	// run it claims to reproduce, and anything downstream that reasons about
	// latency would be reading the replay's speed instead of the crawl's.
	resp := types.NewResponse(req, httpResp, body, e.Duration)

	// NewResponse stamps time.Now(), which on a replay dates the dataset to
	// whenever someone last replayed it. Overwrite with the recorded moment.
	resp.FetchedAt = e.FetchedAt

	return resp, nil
}

// Close is a no-op; a Player holds no sockets.
func (p *Player) Close() error { return nil }

// Len reports how many distinct attempts the log holds.
func (p *Player) Len() int { return len(p.entries) }

// Store exposes the underlying object store.
func (p *Player) Store() *Store { return p.store }

// Coverage reports which of the given URLs the log can serve. Used by `verify` to
// say what a replay would and would not be able to reproduce.
func (p *Player) Coverage(urls []string) (have, missing []string) {
	seen := make(map[string]bool, len(p.entries))
	for k := range p.entries {
		seen[k.url] = true
	}

	for _, u := range urls {
		if seen[u] {
			have = append(have, u)
		} else {
			missing = append(missing, u)
		}
	}
	return have, missing
}

// compile-time checks: both must be usable wherever the engine wants a fetcher.
var (
	_ Fetcher = (*Recorder)(nil)
	_ Fetcher = (*Player)(nil)
)

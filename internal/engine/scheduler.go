package engine

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/extract"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Scheduler manages worker goroutines that dequeue from the frontier and dispatch fetches.
type Scheduler struct {
	engine *Engine
	logger *slog.Logger
	wg     sync.WaitGroup

	// paused is read on the worker fast path without locking; pauseMu guards the
	// resumeCh swap so a worker never parks on a channel that Resume has already
	// closed past.
	paused   atomic.Bool
	pauseMu  sync.RWMutex
	resumeCh chan struct{}

	throttler *Throttler
	breaker   *CircuitBreaker

	// pendingRetries counts requests waiting out a backoff off-queue. They are
	// neither queued nor in flight, so without this the idle monitor would declare
	// the crawl finished and close the frontier while retries were still due —
	// silently dropping them.
	pendingRetries atomic.Int32
	done           chan struct{}
}

// NewScheduler creates a new Scheduler.
func NewScheduler(e *Engine) *Scheduler {
	return &Scheduler{
		engine:   e,
		logger:   e.logger.With("component", "scheduler"),
		resumeCh: make(chan struct{}),
		throttler: NewThrottler(
			e.cfg.Engine.PolitenessDelay,
			e.cfg.Engine.MaxThrottleSlots,
			e.clock,
		),
		breaker: NewCircuitBreaker(
			e.cfg.Engine.CircuitBreakerThreshold,
			e.cfg.Engine.CircuitBreakerCooldown,
			e.cfg.Engine.MaxThrottleSlots,
			e.clock,
		),
		done: make(chan struct{}),
	}
}

// Start launches the worker pool and idle monitor.
func (s *Scheduler) Start(ctx context.Context) {
	concurrency := s.engine.cfg.Engine.Concurrency
	s.logger.Info("starting worker pool", "workers", concurrency)

	for i := 0; i < concurrency; i++ {
		s.wg.Add(1)
		go s.worker(ctx, i)
	}

	// Start idle monitor to detect when all work is done
	go s.idleMonitor(ctx, concurrency)
}

// Wait blocks until all workers are done.
func (s *Scheduler) Wait() {
	s.wg.Wait()
}

// Pause pauses all workers.
//
// The gate channel is swapped under pauseMu rather than reassigned in place: workers
// read it in a select, and an unsynchronised write both races with that read and can
// park a worker on the *new* channel, which the matching Resume has already closed
// past — a permanent missed wakeup.
func (s *Scheduler) Pause() {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()

	if s.paused.Load() {
		return
	}
	s.resumeCh = make(chan struct{})
	s.paused.Store(true)
}

// Resume resumes all workers.
func (s *Scheduler) Resume() {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()

	if !s.paused.Load() {
		return
	}
	s.paused.Store(false)
	close(s.resumeCh)
}

// resumeGate returns the channel a worker should wait on while paused, read under
// the same lock that Pause and Resume write it under.
func (s *Scheduler) resumeGate() <-chan struct{} {
	s.pauseMu.RLock()
	defer s.pauseMu.RUnlock()
	return s.resumeCh
}

// idleMonitor closes the frontier once there is provably no work left.
//
// It used to infer that from how many workers looked idle: idleWorkers was
// incremented before PopReady and decremented after, so a worker holding a
// just-dequeued request was briefly counted as idle. With an empty queue at that
// instant, the monitor could see every worker idle and nothing queued while a fetch
// was about to begin, and end the crawl underneath it. Rare, timing-dependent, and
// indistinguishable from a crawl that had genuinely finished.
//
// Frontier.Outstanding counts queued and in-flight requests together under the
// frontier's own lock, and the in-flight increment happens at the moment of removal,
// so there is no window in which a request is invisible. That makes the completion
// test a fact rather than an inference.
//
// The confirmation streak survives, with a narrower job. It no longer papers over
// the dequeue window; it only covers work that arrives after the crawl looks empty —
// principally a caller that adds seeds after Start rather than before.
func (s *Scheduler) idleMonitor(ctx context.Context, _ int) {
	ticker := s.engine.clock.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	idleStreak := 0

	for {
		select {
		case <-ctx.Done():
			s.engine.frontier.Close()
			return
		case <-s.done:
			return
		case <-ticker.C:
			outstanding := s.engine.frontier.Outstanding()
			pending := int(s.pendingRetries.Load())

			if outstanding == 0 && pending == 0 {
				idleStreak++
				if idleStreak >= 3 {
					s.logger.Info("no queued or in-flight requests — crawl complete")
					s.engine.frontier.Close()
					return
				}
			} else {
				idleStreak = 0
			}
		}
	}
}

// worker is a single crawl worker goroutine.
func (s *Scheduler) worker(ctx context.Context, id int) {
	defer s.wg.Done()
	logger := s.logger.With("worker_id", id)

	for {
		// Check if paused. Read the gate through resumeGate so the channel we park
		// on is the one Resume will close.
		if s.paused.Load() {
			logger.Debug("worker paused")
			select {
			case <-ctx.Done():
				return
			case <-s.resumeGate():
				logger.Debug("worker resumed")
			}
		}

		// PopReady blocks until a request whose domain is off cooldown is
		// available, the frontier closes, or ctx is cancelled — no polling, and no
		// waiting out one domain's politeness delay while other domains have work
		// ready. A returned request is already counted in flight by the frontier,
		// so it is never invisible to the completion check.
		req := s.engine.frontier.PopReady(ctx, s.throttler)
		if req == nil {
			// Frontier closed or context cancelled: no more work is coming.
			return
		}

		// Track active worker count
		active := s.engine.stats.ActiveWorkers.Add(1)
		s.engine.metrics.SetActiveWorkers(int(active))
		s.engine.metrics.SetFrontierDepth(s.engine.frontier.Len())

		// Process the request, then release it. Done comes after processRequest
		// returns because links discovered on the page are pushed during it: a
		// parent that stopped counting before its children were queued would leave
		// the frontier momentarily empty and end the crawl one level in.
		s.processRequest(ctx, logger, req)
		s.engine.frontier.Done()

		active = s.engine.stats.ActiveWorkers.Add(-1)
		s.engine.metrics.SetActiveWorkers(int(active))

		// Check max requests limit
		if s.engine.cfg.Engine.MaxRequests > 0 &&
			s.engine.stats.RequestsSent.Load() >= int64(s.engine.cfg.Engine.MaxRequests) {
			logger.Info("max requests reached, stopping")
			s.engine.Stop()
			return
		}
	}
}

// processRequest handles a single request: fetch, parse, extract, enqueue.
func (s *Scheduler) processRequest(ctx context.Context, logger *slog.Logger, req *types.Request) {
	logger = logger.With("url", req.URLString(), "depth", req.Depth)

	// Select fetcher
	fetcherType := req.FetcherType
	if fetcherType == "" {
		fetcherType = s.engine.cfg.Fetcher.Type
	}

	s.engine.mu.RLock()
	fetcher, ok := s.engine.fetchers[fetcherType]
	s.engine.mu.RUnlock()

	if !ok {
		s.engine.stats.RequestsFailed.Add(1)
		logger.Error("no fetcher for type", "fetcher_type", fetcherType)
		return
	}

	// Fetch with timeout
	timeout := s.engine.cfg.Engine.RequestTimeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	fetchCtx, fetchCancel := context.WithTimeout(ctx, timeout)
	defer fetchCancel()

	// The registrable domain, not the hostname: a site that fails on one subdomain
	// is a site that is failing, and fifty subdomains must not each get their own
	// full failure budget before the breaker notices.
	domain := req.RegistrableDomain()

	// A domain that has failed consistently is skipped rather than retried into
	// the ground. Without this, a site that goes down absorbs the entire request
	// budget while the crawler reports the waste as ordinary errors.
	if !s.breaker.Allow(domain) {
		s.engine.stats.RequestsFailed.Add(1)
		s.engine.metrics.RecordRequest(domain, "circuit_open")
		logger.Warn("circuit open, skipping request", "domain", domain)
		return
	}

	s.engine.stats.RequestsSent.Add(1)

	resp, err := fetcher.Fetch(fetchCtx, req)
	if err != nil {
		s.engine.metrics.RecordRequest(domain, "error")
		if state := s.breaker.RecordFailure(domain); state == breakerOpen {
			logger.Warn("circuit opened for domain", "domain", domain)
		}
		s.handleFetchError(logger, req, err)
		return
	}

	s.breaker.RecordSuccess(domain)
	s.engine.stats.ResponsesOK.Add(1)
	s.engine.stats.BytesDownloaded.Add(resp.ContentLength)
	s.engine.metrics.RecordRequest(domain, "ok")
	s.engine.metrics.RecordResponse(domain, fetcherType, resp.StatusCode,
		resp.FetchDuration, resp.ContentLength)
	logger.Debug("fetched", "status", resp.StatusCode, "size", resp.ContentLength, "duration", resp.FetchDuration)

	// Invoke ALL registered callbacks on every response
	s.engine.mu.RLock()
	callbacksCopy := make(map[string]ResponseCallback, len(s.engine.callbacks))
	for k, v := range s.engine.callbacks {
		callbacksCopy[k] = v
	}
	s.engine.mu.RUnlock()

	// Dispatch in name order. Callbacks emit items, so the order they run in is
	// the order items reach the pipeline — and Go randomises map iteration, which
	// would make the output ordering of a multi-callback crawl differ between runs
	// on identical input.
	cbNames := make([]string, 0, len(callbacksCopy))
	for name := range callbacksCopy {
		cbNames = append(cbNames, name)
	}
	sort.Strings(cbNames)

	for _, cbName := range cbNames {
		cb := callbacksCopy[cbName]
		items, newReqs, err := cb(resp)
		if err != nil {
			logger.Warn("callback error", "callback", cbName, "error", err)
			continue
		}
		for _, item := range items {
			item.SpiderName = cbName
			item.Depth = req.Depth
			stampFromResponse(item, resp)
			if !s.emitItem(ctx, item) {
				return
			}
		}
		for _, r := range newReqs {
			r.Depth = req.Depth + 1
			r.ParentURL = req.URLString()
			_ = s.engine.AddRequest(r)
		}
	}

	// One derivation per response, feeding all three outputs: the claims, the
	// corpus record they attach to, and the item the pipeline sees.
	//
	// The record is still written even when derivation fails, which is why the
	// error is logged here rather than returned. What the source said about reuse
	// does not depend on whether extraction succeeded, and a page whose parse
	// broke is a page a corpus especially wants to have recorded.
	var (
		items []*types.Item
		links []string
	)
	if s.engine.parser != nil {
		var assertions []provenance.Assertion
		assertions, links, items = s.derive(logger, resp)
		s.recordProvenance(ctx, resp, assertions)
	} else {
		s.recordProvenance(ctx, resp, nil)
	}

	// Only emit parser items if no callbacks produced items (avoid duplicates)
	if len(callbacksCopy) == 0 {
		for _, item := range items {
			item.Depth = req.Depth
			stampFromResponse(item, resp)
			if !s.emitItem(ctx, item) {
				return
			}
		}
	}
	for _, link := range links {
		newReq, err := types.NewRequest(link)
		if err != nil {
			continue
		}
		newReq.Depth = req.Depth + 1
		newReq.ParentURL = req.URLString()
		_ = s.engine.AddRequest(newReq)
	}
}

// derive runs the parser once and returns both shapes of what it found.
//
// A parser that implements Deriver is asked for assertions and the item is
// projected from them, so the two cannot disagree. One that does not is asked for
// items as before and contributes no assertions — a caller with its own Parser
// keeps working, it simply gets no corpus claims.
func (s *Scheduler) derive(logger *slog.Logger, resp *types.Response) ([]provenance.Assertion, []string, []*types.Item) {
	rules := s.engine.cfg.Parser.Rules

	if d, ok := s.engine.parser.(Deriver); ok {
		assertions, links, err := d.Derive(resp, rules)
		if err != nil {
			logger.Warn("parse error", "error", err)
		}
		var items []*types.Item
		if item := d.ItemFrom(resp.Request.URLString(), assertions); item != nil {
			items = append(items, item)
		}
		return assertions, links, items
	}

	items, links, err := s.engine.parser.Parse(resp, rules)
	if err != nil {
		logger.Warn("parse error", "error", err)
	}
	return nil, links, items
}

// recordProvenance writes one corpus record for a response.
//
// Failures here are logged, never fatal. A corpus is a by-product of the crawl,
// and losing the crawl because a record could not be written would be the wrong
// trade — but losing records silently would be worse, so it is logged loudly
// enough to notice.
func (s *Scheduler) recordProvenance(ctx context.Context, resp *types.Response, assertions []provenance.Assertion) {
	s.engine.mu.RLock()
	w, sink, crawlID := s.engine.corpus, s.engine.assertions, s.engine.crawlID
	s.engine.mu.RUnlock()

	if resp == nil || resp.Request == nil || (w == nil && sink == nil) {
		return
	}

	var (
		doc     *goquery.Document
		content provenance.Content
	)

	// Extraction only makes sense for HTML, and only the text needs it. A non-HTML
	// response still gets a record — it has provenance, just no extracted text.
	if isHTML(resp) {
		// One DOM parse per response. Response.Document caches, so this is the
		// same tree the parser goes on to use for link discovery and rules. This
		// used to build a second document from the same bytes, so every page in
		// every corpus crawl was tokenised and tree-built twice for two outputs
		// that were never joined to each other.
		if d, err := resp.Document(); err == nil {
			doc = d

			// The extractor gets a clone because it destroys what it is given:
			// FromDocument strips script, style, nav, aside, footer and header
			// from its input before scoring, since a <script> body is text as far
			// as the DOM is concerned. Handing it the shared document would delete
			// most of a site's navigation before the parser ran link discovery
			// over it, and the crawl would quietly stop finding pages — no error,
			// just fewer URLs than there should be.
			//
			// The clone is what that safety costs, and it is cheaper than the
			// parse it replaces: ~10µs to clone a typical page against ~76µs to
			// build it again from bytes. One parse plus one clone beats two
			// parses.
			//
			// provenance.Build gets the pristine document, not the clone — it
			// reads <meta> for AI directives, TDM reservations and licence, and
			// those must be read from the page as served.
			if r := extract.New().FromDocument(goquery.CloneDocument(d)); r != nil {
				content = provenance.Content{
					Text:       r.Text,
					Title:      r.Title,
					Confidence: r.Confidence,
				}
			}
		}
	}

	rec := provenance.Build(provenance.Source{
		URL:             resp.Request.URLString(),
		FinalURL:        resp.FinalURL,
		Body:            resp.Body,
		StatusCode:      resp.StatusCode,
		Headers:         resp.Headers,
		FetchedAt:       resp.FetchedAt,
		CrawlerIdentity: resp.Request.Headers.Get("User-Agent"),
		CrawlID:         crawlID,

		// The decision this crawl actually operated under. The request reached a
		// fetcher, which means the robots check upstream permitted it.
		RobotsAllowed: true,
		Robots:        s.engine.robots.Report(resp.Request.URLString()),
	}, doc, content)

	if w != nil {
		if err := w.Write(rec); err != nil {
			s.engine.logger.Warn("could not record provenance",
				"url", resp.Request.URLString(), "error", err)
		}
	}

	if sink == nil || len(assertions) == 0 {
		return
	}

	// Attach every claim to the bytes it was derived from. The record's content
	// hash is the join key, so this is the moment the two halves of the corpus
	// become one thing: without it an assertion is a value with no stated source,
	// which is what the old Item was and what this whole model exists to replace.
	//
	// The evidence range within those bytes is not filled in here. That belongs
	// with each derivation, which knows what text it matched; see ROADMAP.md.
	for _, a := range assertions {
		a.Evidence.ObservationHash = rec.ContentHash
		a.SourceURL = rec.URL
		if err := sink.Write(a); err != nil {
			s.engine.logger.Warn("could not record assertion",
				"url", resp.Request.URLString(), "field", a.Field, "error", err)
			break
		}
	}
}

// isHTML reports whether a response is worth running an HTML extractor over.
func isHTML(resp *types.Response) bool {
	ct := resp.ContentType
	if ct == "" && resp.Headers != nil {
		ct = resp.Headers.Get("Content-Type")
	}
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// stampFromResponse dates an item by when its source was fetched.
//
// types.NewItem stamps time.Now(), which is when the parser happened to run — a
// value that differs between two runs over identical bytes and lands in the
// output as _timestamp. Taking it from the response instead makes the field mean
// "when this data was observed", which is what a consumer of a scraped record
// actually wants, and makes a replayed crawl produce byte-identical output to the
// crawl it replays. Without this the log reproduces the fetches perfectly and the
// dataset still differs on every line.
//
// Applied centrally rather than in each parser so that callbacks and third-party
// parsers get it too, and so there is one place to look when a timestamp is wrong.
func stampFromResponse(item *types.Item, resp *types.Response) {
	if item == nil || resp == nil || resp.FetchedAt.IsZero() {
		return
	}
	item.Timestamp = resp.FetchedAt
}

// emitItem hands an item to the pipeline, giving up if the crawl is shutting down.
//
// A bare send here can block forever once the consumer has stopped draining, and —
// worse — panics with "send on closed channel" if shutdown closes itemChan while a
// worker is mid-send. Reporting the shutdown back to the caller lets the worker
// unwind instead.
func (s *Scheduler) emitItem(ctx context.Context, item *types.Item) bool {
	select {
	case s.engine.itemChan <- item:
		return true
	case <-ctx.Done():
		return false
	}
}

// handleFetchError handles fetch failures with retry logic.
func (s *Scheduler) handleFetchError(logger *slog.Logger, req *types.Request, err error) {
	s.engine.stats.RequestsFailed.Add(1)

	// Check if retryable. errors.As rather than a bare type assertion: any
	// middleware or fetcher that wraps the error with %w would otherwise make every
	// failure look permanent, silently disabling retries.
	var fetchErr *types.FetchError
	ok := errors.As(err, &fetchErr)
	if ok && fetchErr.IsRetryable() && req.RetryCount < req.MaxRetries {
		req.RetryCount++
		req.Priority = types.PriorityLow // Lower priority for retries
		logger.Warn("retrying request",
			"retry", req.RetryCount,
			"max_retries", req.MaxRetries,
			"error", err,
		)
		// Back off before the request becomes eligible again. A server's
		// Retry-After wins when present; otherwise exponential with full jitter.
		delay := backoffFor(s.engine.rand, s.engine.cfg.Engine.RetryDelay, req.RetryCount)
		if fetchErr.RetryAfter > delay {
			delay = fetchErr.RetryAfter
			logger.Info("rate limited — honouring Retry-After",
				"retry_after", fetchErr.RetryAfter,
				"url", req.URLString(),
			)
		}

		// Same key as the other RecordRequest calls, so retries line up with the
		// successes and failures for the domain rather than landing on a separate
		// per-subdomain series.
		s.engine.metrics.RecordRequest(req.RegistrableDomain(), "retry")

		// Re-queued on a timer rather than by sleeping here: sleeping inside the
		// worker is what the politeness throttle used to do, and it holds a
		// worker slot hostage for up to two minutes doing nothing.
		if delay > 0 {
			s.requeueAfter(delay, req)
		} else {
			s.engine.frontier.Push(req)
		}
		return
	}

	s.engine.stats.ResponsesError.Add(1)
	logger.Error("fetch failed permanently", "error", err, "retries", req.RetryCount)
}

// requeueAfter pushes req back onto the frontier once delay has elapsed, without
// occupying a worker while it waits.
//
// The goroutine is tracked by the scheduler's WaitGroup so shutdown does not race
// it, and it gives up on context cancellation rather than re-queuing work into a
// crawl that is already ending.
func (s *Scheduler) requeueAfter(delay time.Duration, req *types.Request) {
	s.wg.Add(1)
	s.pendingRetries.Add(1)

	go func() {
		defer s.wg.Done()
		defer s.pendingRetries.Add(-1)

		timer := s.engine.clock.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-timer.C:
			s.engine.frontier.Push(req)
		case <-s.engine.ctx.Done():
		case <-s.done:
		}
	}()
}

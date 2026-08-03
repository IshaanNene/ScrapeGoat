package engine

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

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

	throttler   *Throttler
	breaker     *CircuitBreaker
	idleWorkers atomic.Int32

	// pendingRetries counts requests waiting out a backoff off-queue. They are
	// invisible to both the frontier length and the idle-worker count, so
	// without this the idle monitor would declare the crawl finished and close
	// the frontier while retries were still due — silently dropping them.
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

// idleMonitor checks if all workers are idle and frontier is empty.
// When this condition holds for a sustained period, it closes the frontier.
func (s *Scheduler) idleMonitor(ctx context.Context, concurrency int) {
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
			idle := int(s.idleWorkers.Load())
			queueLen := s.engine.frontier.Len()

			pending := int(s.pendingRetries.Load())

			if idle >= concurrency && queueLen == 0 && pending == 0 {
				idleStreak++
				// Require 3 consecutive idle checks (~600ms) to confirm completion
				if idleStreak >= 3 {
					s.logger.Info("all workers idle, frontier empty — crawl complete")
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

		// Mark as idle while parked on the frontier. PopReady blocks until a
		// request whose domain is off cooldown is available, the frontier closes,
		// or ctx is cancelled — no polling, and no waiting out one domain's
		// politeness delay while other domains have work ready.
		s.idleWorkers.Add(1)
		req := s.engine.frontier.PopReady(ctx, s.throttler)
		s.idleWorkers.Add(-1)

		if req == nil {
			// Frontier closed or context cancelled: no more work is coming.
			return
		}

		// Track active worker count
		active := s.engine.stats.ActiveWorkers.Add(1)
		s.engine.metrics.SetActiveWorkers(int(active))
		s.engine.metrics.SetFrontierDepth(s.engine.frontier.Len())

		// Process the request
		s.processRequest(ctx, logger, req)

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

	domain := req.Domain()

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

	// Always run the parser for link discovery and structured data
	if s.engine.parser != nil {
		items, links, err := s.engine.parser.Parse(resp, s.engine.cfg.Parser.Rules)
		if err != nil {
			logger.Warn("parse error", "error", err)
		}
		// Only emit parser items if no callbacks produced items (avoid duplicates)
		if len(callbacksCopy) == 0 {
			for _, item := range items {
				item.Depth = req.Depth
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

		s.engine.metrics.RecordRequest(req.Domain(), "retry")

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

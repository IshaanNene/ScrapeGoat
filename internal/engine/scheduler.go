package engine

import (
	"context"
	"errors"
	"log/slog"
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

	throttle    map[string]*domainThrottle
	throttleMu  sync.RWMutex
	idleWorkers atomic.Int32
	done        chan struct{}
}

// domainThrottle implements per-domain rate limiting.
type domainThrottle struct {
	lastFetch time.Time
	mu        sync.Mutex
}

// NewScheduler creates a new Scheduler.
func NewScheduler(e *Engine) *Scheduler {
	return &Scheduler{
		engine:   e,
		logger:   e.logger.With("component", "scheduler"),
		resumeCh: make(chan struct{}),
		throttle: make(map[string]*domainThrottle),
		done:     make(chan struct{}),
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
	ticker := time.NewTicker(200 * time.Millisecond)
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

			if idle >= concurrency && queueLen == 0 {
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

		// Mark as idle while parked on the frontier. Pop blocks until a Push wakes
		// it, the frontier closes, or ctx is cancelled — no polling.
		s.idleWorkers.Add(1)
		req := s.engine.frontier.Pop(ctx)
		s.idleWorkers.Add(-1)

		if req == nil {
			// Frontier closed or context cancelled: no more work is coming.
			return
		}

		// Apply per-domain throttle
		s.applyThrottle(req.Domain())

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

	s.engine.stats.RequestsSent.Add(1)
	domain := req.Domain()

	resp, err := fetcher.Fetch(fetchCtx, req)
	if err != nil {
		s.engine.metrics.RecordRequest(domain, "error")
		s.handleFetchError(logger, req, err)
		return
	}

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

	for cbName, cb := range callbacksCopy {
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
		// For 429: respect Retry-After before re-queuing
		if fetchErr.RetryAfter > 0 {
			logger.Info("rate limited — backing off",
				"retry_after", fetchErr.RetryAfter,
				"url", req.URLString(),
			)
			time.Sleep(fetchErr.RetryAfter)
		}
		s.engine.metrics.RecordRequest(req.Domain(), "retry")
		s.engine.frontier.Push(req)
		return
	}

	s.engine.stats.ResponsesError.Add(1)
	logger.Error("fetch failed permanently", "error", err, "retries", req.RetryCount)
}

// applyThrottle enforces per-domain politeness delays.
func (s *Scheduler) applyThrottle(domain string) {
	delay := s.engine.cfg.Engine.PolitenessDelay
	if delay <= 0 {
		return
	}

	s.throttleMu.Lock()
	t, ok := s.throttle[domain]
	if !ok {
		t = &domainThrottle{}
		s.throttle[domain] = t
	}
	s.throttleMu.Unlock()

	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.lastFetch)
	if elapsed < delay {
		time.Sleep(delay - elapsed)
	}
	t.lastFetch = time.Now()
}

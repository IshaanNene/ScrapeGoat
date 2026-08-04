package engine

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/clock"
	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/observability"
	"github.com/IshaanNene/ScrapeGoat/internal/safety"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// State represents the engine's current lifecycle state.
type State int32

const (
	StateIdle     State = 0
	StateRunning  State = 1
	StatePaused   State = 2
	StateStopping State = 3
	StateStopped  State = 4
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StatePaused:
		return "paused"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Stats tracks crawl statistics.
type Stats struct {
	RequestsSent    atomic.Int64
	RequestsFailed  atomic.Int64
	ResponsesOK     atomic.Int64
	ResponsesError  atomic.Int64
	ItemsScraped    atomic.Int64
	ItemsDropped    atomic.Int64
	URLsEnqueued    atomic.Int64
	URLsFiltered    atomic.Int64
	BytesDownloaded atomic.Int64
	ActiveWorkers   atomic.Int32
	StartTime       time.Time
	mu              sync.RWMutex
	domainStats     map[string]*DomainStats
}

// DomainStats tracks per-domain statistics.
type DomainStats struct {
	Requests  int64
	Responses int64
	Errors    int64
	LastFetch time.Time
}

// Snapshot returns a copy of stats safe for reading.
//
// Deliberately does not compute an "elapsed" field. Stats has no clock of its own,
// and reaching for the wall clock here would put a nondeterministic value in the
// middle of the engine's own status output. Engine.Stats adds elapsed from the
// injected clock instead.
func (s *Stats) Snapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"requests_sent":    s.RequestsSent.Load(),
		"requests_failed":  s.RequestsFailed.Load(),
		"responses_ok":     s.ResponsesOK.Load(),
		"responses_error":  s.ResponsesError.Load(),
		"items_scraped":    s.ItemsScraped.Load(),
		"items_dropped":    s.ItemsDropped.Load(),
		"urls_enqueued":    s.URLsEnqueued.Load(),
		"urls_filtered":    s.URLsFiltered.Load(),
		"bytes_downloaded": s.BytesDownloaded.Load(),
		"active_workers":   s.ActiveWorkers.Load(),
	}
}

// Fetcher is the interface for all fetcher implementations.
type Fetcher interface {
	Fetch(ctx context.Context, req *types.Request) (*types.Response, error)
	Close() error
}

// Parser is the interface for all parser implementations.
type Parser interface {
	Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error)
}

// Pipeline is the interface for the item processing pipeline.
type Pipeline interface {
	Process(item *types.Item) (*types.Item, error)
}

// Storage is the interface for all storage backends.
type Storage interface {
	Store(items []*types.Item) error
	Close() error
}

// ResponseCallback is a function called when a response is received.
type ResponseCallback func(resp *types.Response) ([]*types.Item, []*types.Request, error)

// Engine is the core crawler orchestrator.
type Engine struct {
	cfg        *config.Config
	logger     *slog.Logger
	frontier   *Frontier
	dedup      Deduper
	robots     *RobotsManager
	checkpoint *CheckpointManager
	scheduler  *Scheduler
	fetchers   map[string]Fetcher
	parser     Parser
	pipeline   Pipeline
	storage    Storage

	state      atomic.Int32
	stats      *Stats
	callbacks  map[string]ResponseCallback
	itemChan   chan *types.Item
	resultChan chan *types.Item

	// subscribers receive a copy of every stored item. Kept separate from
	// resultChan so that a consumer of ResultsChan does not compete with storage
	// for items — see ResultsChan.
	subscribers []chan *types.Item
	subMu       sync.Mutex

	// clock and rand are the engine's controlled sources of nondeterminism.
	// Everything in the crawl path takes time and entropy from here rather than
	// from the standard library, so that a crawl can be replayed. See
	// docs/design/0001-deterministic-crawl.md.
	clock clock.Clock
	rand  *rand.Rand

	// metrics may be nil, in which case every recording call is a no-op. This is
	// the wiring the previous code was missing entirely: cmd/scrapegoat built a
	// Metrics in a local variable, started its HTTP server, and never handed it to
	// the engine — so the endpoint served permanently-zero counters.
	metrics *observability.Metrics

	shutdownOnce sync.Once
	shutdownDone chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
}

// New creates a new Engine with the given configuration.
//
// Options are for controlling nondeterminism (clock, randomness); the defaults are
// the real ones, so callers who do not care can ignore them entirely.
func New(cfg *config.Config, logger *slog.Logger, opts ...Option) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	o := resolve(opts)

	e := &Engine{
		clock:    o.clock,
		rand:     o.rand,
		cfg:      cfg,
		logger:   logger,
		frontier: NewFrontier(o.clock),
		dedup:    newDeduper(cfg, logger),
		robots: NewRobotsManager(cfg.Engine.RespectRobotsTxt, safety.New(safety.Config{
			AllowedSchemes:        cfg.Safety.AllowedSchemes,
			AllowPrivateAddresses: cfg.Safety.AllowPrivateAddresses,
			AllowedPrivateHosts:   cfg.Safety.AllowedPrivateHosts,
		}), o.clock),
		checkpoint:   NewCheckpointManager(cfg.Engine.CheckpointInterval, o.clock),
		fetchers:     make(map[string]Fetcher),
		callbacks:    make(map[string]ResponseCallback),
		itemChan:     make(chan *types.Item, cfg.Engine.Concurrency*10),
		resultChan:   make(chan *types.Item, cfg.Engine.Concurrency*10),
		shutdownDone: make(chan struct{}),
		stats: &Stats{
			domainStats: make(map[string]*DomainStats),
		},
		ctx:    ctx,
		cancel: cancel,
	}

	e.scheduler = NewScheduler(e)
	return e
}

// SetFetcher registers a fetcher for a given type.
func (e *Engine) SetFetcher(fetcherType string, f Fetcher) {
	e.mu.Lock()
	e.fetchers[fetcherType] = f
	e.mu.Unlock()

	// robots.txt goes over the same transport as the pages it governs. Routing it
	// through the registered fetcher means a recorded crawl records its robots
	// fetches too, and a replay answers "was this allowed?" from the log instead
	// of from the live site — without which a replay is neither offline nor a
	// faithful account of the decisions the original crawl made.
	if fetcherType == "http" && e.robots != nil {
		e.robots.SetFetcher(f)
	}
}

// SetParser sets the parser implementation.
func (e *Engine) SetParser(p Parser) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.parser = p
}

// SetPipeline sets the pipeline implementation.
func (e *Engine) SetPipeline(p Pipeline) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pipeline = p
}

// SetMetrics attaches a Prometheus metrics recorder. Call before Start.
// Passing nil (or never calling this) disables metric recording.
func (e *Engine) SetMetrics(m *observability.Metrics) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.metrics = m
}

// SetStorage sets the storage implementation.
func (e *Engine) SetStorage(s Storage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.storage = s
}

// OnResponse registers a named callback for response processing.
func (e *Engine) OnResponse(name string, cb ResponseCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.callbacks[name] = cb
}

// AddSeed adds a seed URL to the crawl frontier.
func (e *Engine) AddSeed(rawURL string) error {
	req, err := types.NewRequest(rawURL)
	if err != nil {
		return err
	}
	req.Priority = types.PriorityHighest
	req.Depth = 0
	return e.AddRequest(req)
}

// AddRequest adds a request to the crawl frontier.
func (e *Engine) AddRequest(req *types.Request) error {
	urlStr := req.URLString()

	// Check depth (seed pages are depth 0; discovered links are depth 1+)
	if req.Depth > e.cfg.Engine.MaxDepth {
		e.stats.URLsFiltered.Add(1)
		return types.ErrMaxDepth
	}

	// Check robots.txt
	if e.cfg.Engine.RespectRobotsTxt && !e.robots.IsAllowed(urlStr) {
		e.stats.URLsFiltered.Add(1)
		return types.ErrBlocked
	}

	// Check domain filters
	if !e.isDomainAllowed(req.Domain()) {
		e.stats.URLsFiltered.Add(1)
		return fmt.Errorf("domain %q is not allowed", req.Domain())
	}

	// Claim the URL atomically. AddRequest runs on worker goroutines during link
	// extraction, so a separate IsSeen check followed by MarkSeen would let two
	// workers that found the same link both pass and both enqueue it.
	//
	// This runs last among the filters so that a URL rejected by robots.txt or the
	// domain allowlist is not recorded as seen — the same URL may legitimately
	// arrive later with a different depth or after robots.txt is re-read.
	if !e.dedup.MarkIfUnseen(urlStr) {
		e.stats.URLsFiltered.Add(1)
		return types.ErrDuplicate
	}

	e.frontier.Push(req)
	e.stats.URLsEnqueued.Add(1)
	e.metrics.SetFrontierDepth(e.frontier.Len())
	return nil
}

// Start begins crawling.
func (e *Engine) Start() error {
	if !e.state.CompareAndSwap(int32(StateIdle), int32(StateRunning)) {
		return fmt.Errorf("engine is in state %s, cannot start", State(e.state.Load()))
	}

	e.logger.Info("engine starting",
		"concurrency", e.cfg.Engine.Concurrency,
		"max_depth", e.cfg.Engine.MaxDepth,
		"respect_robots", e.cfg.Engine.RespectRobotsTxt,
	)

	e.stats.StartTime = e.clock.Now()

	// Start item pipeline processor
	e.wg.Add(1)
	go e.processItems()

	// Start result storage processor
	e.wg.Add(1)
	go e.storeResults()

	// Start checkpoint auto-save
	if e.cfg.Engine.CheckpointInterval > 0 {
		e.wg.Add(1)
		go e.autoCheckpoint()
	}

	// Start scheduler (worker pool)
	e.scheduler.Start(e.ctx)

	return nil
}

// Wait blocks until all work is done.
//
// Safe to call more than once: the shutdown body runs under a sync.Once, so a second
// Wait blocks until the first has finished rather than panicking on a double channel
// close. Callers reasonably treat Wait as idempotent, and "close of closed channel"
// is a poor way to find out otherwise.
func (e *Engine) Wait() {
	e.scheduler.Wait()

	e.shutdownOnce.Do(func() {
		// Cancel context to stop checkpoint goroutine and other background tasks
		e.cancel()

		// Signal processors to stop. itemChan is closed here, after every worker has
		// returned from scheduler.Wait(), so no worker can still be mid-send.
		close(e.itemChan)

		e.wg.Wait()
		e.state.Store(int32(StateStopped))

		// Close fetchers
		e.mu.RLock()
		for _, f := range e.fetchers {
			if err := f.Close(); err != nil {
				e.logger.Error("fetcher close error", "error", err)
			}
		}
		e.mu.RUnlock()

		e.logger.Info("engine stopped", "stats", e.StatsSnapshot())
		close(e.shutdownDone)
	})

	<-e.shutdownDone
}

// Stop gracefully stops the engine.
func (e *Engine) Stop() {
	if !e.state.CompareAndSwap(int32(StateRunning), int32(StateStopping)) {
		return
	}
	e.logger.Info("engine stopping...")
	// Close frontier first so all workers polling TryPop() see IsClosed() and exit
	e.frontier.Close()
	e.cancel()
}

// Pause pauses the engine.
func (e *Engine) Pause() {
	if e.state.CompareAndSwap(int32(StateRunning), int32(StatePaused)) {
		e.logger.Info("engine paused")
		e.scheduler.Pause()
	}
}

// Resume resumes a paused engine.
func (e *Engine) Resume() {
	if e.state.CompareAndSwap(int32(StatePaused), int32(StateRunning)) {
		e.logger.Info("engine resumed")
		e.scheduler.Resume()
	}
}

// Stats returns the current crawl statistics.
func (e *Engine) Stats() *Stats {
	return e.stats
}

// StatsSnapshot returns the crawl statistics plus an "elapsed" field measured on
// the engine's clock.
//
// Stats.Snapshot deliberately omits elapsed: Stats has no clock, and reaching for
// the wall clock there would put a nondeterministic value in the engine's own
// status output. The engine has a clock, so it adds it here.
func (e *Engine) StatsSnapshot() map[string]any {
	snap := e.stats.Snapshot()
	snap["elapsed"] = e.clock.Since(e.stats.StartTime).String()
	return snap
}

// State returns the current engine state.
func (e *Engine) GetState() State {
	return State(e.state.Load())
}

// ResultsChan returns a channel that receives a copy of every scraped item.
//
// Each call registers an independent subscriber, so multiple consumers each see the
// full stream and none of them competes with storage. Previously this handed back
// the same channel storeResults was draining, which meant every item went to exactly
// one of them at random: reading the "results" silently corrupted the output file.
//
// Call this before Start. Subscribers registered mid-crawl only see items scraped
// after they register. The channel is closed when the crawl finishes.
//
// A slow subscriber applies backpressure to the crawl once its buffer fills, which is
// deliberate — dropping items to keep a consumer fast is a worse failure than
// slowing down.
func (e *Engine) ResultsChan() <-chan *types.Item {
	ch := make(chan *types.Item, e.cfg.Engine.Concurrency*10)

	e.subMu.Lock()
	e.subscribers = append(e.subscribers, ch)
	e.subMu.Unlock()

	return ch
}

// fanOut delivers an item to every registered subscriber.
//
// The sends block rather than selecting on ctx.Done: Stop cancels the context to
// halt fetching, and bailing out here would silently discard items that were already
// scraped and paid for. A subscriber that stops reading before the channel closes
// stalls the crawl, which is the documented contract and a far more debuggable
// failure than losing rows from the output.
func (e *Engine) fanOut(item *types.Item) {
	e.subMu.Lock()
	subs := e.subscribers
	e.subMu.Unlock()

	for _, ch := range subs {
		ch <- item
	}
}

// closeSubscribers closes every subscriber channel exactly once, at end of crawl.
func (e *Engine) closeSubscribers() {
	e.subMu.Lock()
	subs := e.subscribers
	e.subscribers = nil
	e.subMu.Unlock()

	for _, ch := range subs {
		close(ch)
	}
}

// processItems runs the pipeline on scraped items.
func (e *Engine) processItems() {
	defer e.wg.Done()
	// Deferred so that an early return on cancellation still releases storeResults
	// and every subscriber, rather than leaving them ranging over a channel that
	// will never close.
	defer e.closeSubscribers()
	defer close(e.resultChan)

	for item := range e.itemChan {
		if e.pipeline != nil {
			processed, err := e.pipeline.Process(item)
			if err != nil {
				e.stats.ItemsDropped.Add(1)
				e.metrics.RecordItem("dropped")
				e.logger.Warn("pipeline dropped item", "url", item.URL, "error", err)
				continue
			}
			item = processed
		}
		e.stats.ItemsScraped.Add(1)
		e.metrics.RecordItem("scraped")

		// Storage is the primary consumer; subscribers get their own copies.
		// Both sends block: itemChan is closed once every worker has exited, so this
		// loop is guaranteed to terminate, and abandoning it on cancellation would
		// throw away items that have already been fetched and parsed.
		e.resultChan <- item
		e.fanOut(item)
	}
}

// storeResults persists items from the result channel.
func (e *Engine) storeResults() {
	defer e.wg.Done()
	batch := make([]*types.Item, 0, e.cfg.Storage.BatchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if e.storage != nil {
			if err := e.storage.Store(batch); err != nil {
				e.logger.Error("storage error", "error", err, "batch_size", len(batch))
			} else {
				for range batch {
					e.metrics.RecordItem("stored")
				}
			}
		}
		batch = batch[:0]
	}

	for item := range e.resultChan {
		batch = append(batch, item)
		if len(batch) >= e.cfg.Storage.BatchSize {
			flush()
		}
	}
	flush()

	if e.storage != nil {
		if err := e.storage.Close(); err != nil {
			e.logger.Error("storage close error", "error", err)
		}
	}
}

// autoCheckpoint periodically saves engine state.
func (e *Engine) autoCheckpoint() {
	defer e.wg.Done()
	ticker := e.clock.NewTicker(e.cfg.Engine.CheckpointInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			// Save final checkpoint on shutdown
			if err := e.checkpoint.Save(e.frontier, e.dedup, e.stats); err != nil {
				e.logger.Error("final checkpoint save failed", "error", err)
			}
			return
		case <-ticker.C:
			if err := e.checkpoint.Save(e.frontier, e.dedup, e.stats); err != nil {
				e.logger.Error("checkpoint save failed", "error", err)
			} else {
				e.logger.Debug("checkpoint saved")
			}
		}
	}
}

// HasCheckpoint reports whether a checkpoint from a previous run is available.
func (e *Engine) HasCheckpoint() bool { return e.checkpoint.HasCheckpoint() }

// ResumeFromCheckpoint restores frontier, dedup, and stats from the last
// checkpoint. Distinct from Resume, which un-pauses a running engine.
//
// Call before Start and before adding seeds. Checkpointing was previously
// write-only: Save ran on a ticker and Load had no caller outside tests, so the
// crawler produced checkpoint files that nothing ever read.
//
// Seeds added after a resume are still filtered through the restored dedup set,
// so re-running the same command with the same seeds does not re-crawl what the
// previous run already covered — the restored frontier is the remaining work, and
// the seeds are a no-op unless they are genuinely new.
func (e *Engine) ResumeFromCheckpoint() error {
	if !e.checkpoint.HasCheckpoint() {
		return fmt.Errorf("no checkpoint found in %s", e.checkpoint.Dir())
	}

	if err := e.checkpoint.Load(e.frontier, e.dedup, e.stats); err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}

	e.logger.Info("resumed from checkpoint",
		"queued", e.frontier.Len(),
		"seen", e.dedup.Count(),
		"requests_sent", e.stats.RequestsSent.Load(),
	)
	e.metrics.SetFrontierDepth(e.frontier.Len())
	return nil
}

// ClearCheckpoint removes the checkpoint file. Called after a crawl finishes
// normally, so the next run does not resume work that is already done.
func (e *Engine) ClearCheckpoint() error { return e.checkpoint.Clean() }

// newDeduper builds the deduplication strategy named in the configuration.
//
// The default is exact. Bloom is opt-in because it is lossy: a false positive
// means a URL is treated as already-seen and never crawled, which is a silent
// data-completeness cost, not a performance knob. Engine.New previously hardcoded
// NewDeduplicator(1_000_000) — so the Bloom implementation, its tests, and its
// documented memory saving were all unreachable.
func newDeduper(cfg *config.Config, logger *slog.Logger) Deduper {
	expected := cfg.Engine.ExpectedURLs
	if expected <= 0 {
		expected = 1_000_000
	}

	switch strings.ToLower(cfg.Engine.DedupStrategy) {
	case "bloom":
		logger.Warn("using bloom deduplication — memory-bounded but lossy",
			"expected_urls", expected,
			"target_fp_rate", cfg.Engine.DedupFPRate,
			"note", "a false positive silently skips a URL that was never crawled")
		return NewBloomDeduplicator(expected, cfg.Engine.DedupFPRate)
	default:
		return NewDeduplicator(expected)
	}
}

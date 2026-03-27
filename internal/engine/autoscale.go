package engine

import (
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// AutoscaleConfig configures the autoscaled worker pool.
type AutoscaleConfig struct {
	// MinConcurrency is the minimum number of workers.
	MinConcurrency int

	// MaxConcurrency is the maximum number of workers.
	MaxConcurrency int

	// ScaleUpThreshold: if system load (0.0-1.0) is below this, add workers.
	ScaleUpThreshold float64

	// ScaleDownThreshold: if system load is above this, remove workers.
	ScaleDownThreshold float64

	// CheckInterval is how often to evaluate scaling decisions.
	CheckInterval time.Duration

	// CooldownPeriod is the minimum time between scaling actions.
	CooldownPeriod time.Duration
}

// DefaultAutoscaleConfig returns sensible defaults.
func DefaultAutoscaleConfig() *AutoscaleConfig {
	numCPU := runtime.NumCPU()
	return &AutoscaleConfig{
		MinConcurrency:     2,
		MaxConcurrency:     numCPU * 10,
		ScaleUpThreshold:   0.6,  // Scale up when load < 60%
		ScaleDownThreshold: 0.85, // Scale down when load > 85%
		CheckInterval:      2 * time.Second,
		CooldownPeriod:     5 * time.Second,
	}
}

// AutoscaledPool dynamically adjusts worker concurrency based on system load.
// Inspired by Crawlee's AutoscaledPool — ramp up when idle, back off when strained.
type AutoscaledPool struct {
	config     *AutoscaleConfig
	current    atomic.Int32
	desired    atomic.Int32
	lastAction time.Time
	logger     *slog.Logger
	mu         sync.Mutex

	// Metrics for scaling decisions
	activeWorkers atomic.Int32
	idleWorkers   atomic.Int32
	queueSize     func() int // callback to check frontier size
}

// NewAutoscaledPool creates an autoscaled pool.
func NewAutoscaledPool(cfg *AutoscaleConfig, queueSizeFn func() int, logger *slog.Logger) *AutoscaledPool {
	if cfg == nil {
		cfg = DefaultAutoscaleConfig()
	}

	pool := &AutoscaledPool{
		config:    cfg,
		queueSize: queueSizeFn,
		logger:    logger.With("component", "autoscale"),
	}
	pool.current.Store(int32(cfg.MinConcurrency)) // nolint:gosec // Always small positive int
	pool.desired.Store(int32(cfg.MinConcurrency)) // nolint:gosec // Always small positive int

	return pool
}

// CurrentConcurrency returns the current worker count.
func (p *AutoscaledPool) CurrentConcurrency() int {
	return int(p.current.Load())
}

// DesiredConcurrency returns the target worker count after the last evaluation.
func (p *AutoscaledPool) DesiredConcurrency() int {
	return int(p.desired.Load())
}

// RecordWorkerActive increments the active worker counter.
func (p *AutoscaledPool) RecordWorkerActive() {
	p.activeWorkers.Add(1)
	p.idleWorkers.Add(-1)
}

// RecordWorkerIdle increments the idle worker counter.
func (p *AutoscaledPool) RecordWorkerIdle() {
	p.activeWorkers.Add(-1)
	p.idleWorkers.Add(1)
}

// SetWorkerCounts sets the current worker counts directly.
func (p *AutoscaledPool) SetWorkerCounts(active, idle int) {
	p.activeWorkers.Store(int32(active)) // nolint:gosec // Always small positive int
	p.idleWorkers.Store(int32(idle))   // nolint:gosec // Always small positive int
}

// Evaluate checks current system load and decides whether to scale up or down.
// Returns the new desired concurrency. Call this periodically from a monitoring goroutine.
func (p *AutoscaledPool) Evaluate() ScaleDecision {
	p.mu.Lock()
	defer p.mu.Unlock()

	current := int(p.current.Load())
	active := int(p.activeWorkers.Load())
	idle := int(p.idleWorkers.Load())
	total := active + idle
	if total == 0 {
		total = current
	}

	queueLen := 0
	if p.queueSize != nil {
		queueLen = p.queueSize()
	}

	// Calculate utilization
	utilization := float64(0)
	if total > 0 {
		utilization = float64(active) / float64(total)
	}

	// System load factor (memory pressure)
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memUsageMB := float64(memStats.Alloc) / 1024 / 1024
	memPressure := memUsageMB / 1024 // normalize against 1GB baseline

	// Combined load score
	loadScore := utilization*0.7 + memPressure*0.3

	decision := ScaleDecision{
		Current:     current,
		Desired:     current,
		Utilization: utilization,
		LoadScore:   loadScore,
		QueueSize:   queueLen,
		Action:      ScaleNone,
	}

	// Check cooldown
	if time.Since(p.lastAction) < p.config.CooldownPeriod {
		return decision
	}

	// Scale up: low load + items in queue
	if loadScore < p.config.ScaleUpThreshold && queueLen > current && current < p.config.MaxConcurrency {
		newCount := min(current+scaleStep(current), p.config.MaxConcurrency)
		decision.Desired = newCount
		decision.Action = ScaleUp
		p.desired.Store(int32(newCount)) // nolint:gosec // Always small positive int
		p.current.Store(int32(newCount)) // nolint:gosec // Always small positive int
		p.lastAction = time.Now()
		p.logger.Info("scaling up",
			"from", current, "to", newCount,
			"utilization", fmt.Sprintf("%.1f%%", utilization*100),
			"queue", queueLen,
		)
	}

	// Scale down: high load or empty queue
	if loadScore > p.config.ScaleDownThreshold && current > p.config.MinConcurrency {
		newCount := max(current-scaleStep(current), p.config.MinConcurrency)
		decision.Desired = newCount
		decision.Action = ScaleDown
		p.desired.Store(int32(newCount))
		p.current.Store(int32(newCount))
		p.lastAction = time.Now()
		p.logger.Info("scaling down",
			"from", current, "to", newCount,
			"utilization", fmt.Sprintf("%.1f%%", utilization*100),
			"load_score", fmt.Sprintf("%.2f", loadScore),
		)
	}

	return decision
}

// ScaleAction represents a scaling direction.
type ScaleAction int

const (
	ScaleNone ScaleAction = iota
	ScaleUp
	ScaleDown
)

// ScaleDecision describes the outcome of an Evaluate call.
type ScaleDecision struct {
	Current     int
	Desired     int
	Utilization float64
	LoadScore   float64
	QueueSize   int
	Action      ScaleAction
}

// scaleStep determines how many workers to add/remove.
// Larger pools scale more aggressively.
func scaleStep(current int) int {
	switch {
	case current < 5:
		return 1
	case current < 20:
		return 2
	case current < 50:
		return 5
	default:
		return 10
	}
}

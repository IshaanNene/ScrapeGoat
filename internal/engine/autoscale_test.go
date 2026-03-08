package engine

import (
	"io"
	"log/slog"
	"testing"
)

var testAutoscaleLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

func TestDefaultAutoscaleConfig(t *testing.T) {
	cfg := DefaultAutoscaleConfig()
	if cfg.MinConcurrency < 1 {
		t.Errorf("expected MinConcurrency >= 1, got %d", cfg.MinConcurrency)
	}
	if cfg.MaxConcurrency < cfg.MinConcurrency {
		t.Errorf("expected MaxConcurrency >= MinConcurrency, got %d < %d", cfg.MaxConcurrency, cfg.MinConcurrency)
	}
	if cfg.ScaleUpThreshold <= 0 || cfg.ScaleUpThreshold >= 1 {
		t.Errorf("expected ScaleUpThreshold in (0,1), got %f", cfg.ScaleUpThreshold)
	}
	if cfg.ScaleDownThreshold <= cfg.ScaleUpThreshold {
		t.Errorf("expected ScaleDownThreshold > ScaleUpThreshold, got %f <= %f", cfg.ScaleDownThreshold, cfg.ScaleUpThreshold)
	}
}

func TestAutoscaledPoolCreation(t *testing.T) {
	cfg := &AutoscaleConfig{
		MinConcurrency:     2,
		MaxConcurrency:     20,
		ScaleUpThreshold:   0.5,
		ScaleDownThreshold: 0.9,
		CooldownPeriod:     0, // no cooldown for testing
	}

	pool := NewAutoscaledPool(cfg, func() int { return 0 }, testAutoscaleLogger)

	if pool.CurrentConcurrency() != 2 {
		t.Errorf("expected initial concurrency 2, got %d", pool.CurrentConcurrency())
	}
	if pool.DesiredConcurrency() != 2 {
		t.Errorf("expected initial desired 2, got %d", pool.DesiredConcurrency())
	}
}

func TestAutoscaleScaleUp(t *testing.T) {
	queueSize := 100
	cfg := &AutoscaleConfig{
		MinConcurrency:     2,
		MaxConcurrency:     20,
		ScaleUpThreshold:   0.6,
		ScaleDownThreshold: 0.9,
		CooldownPeriod:     0,
	}

	pool := NewAutoscaledPool(cfg, func() int { return queueSize }, testAutoscaleLogger)

	// Simulate low utilization: 1 active, 9 idle
	pool.SetWorkerCounts(1, 9)

	decision := pool.Evaluate()
	if decision.Action != ScaleUp {
		t.Errorf("expected ScaleUp, got %v", decision.Action)
	}
	if decision.Desired <= decision.Current {
		// Current should have been 2, desired should be > 2
		t.Errorf("expected desired > initial, got desired=%d, current_before=%d", decision.Desired, 2)
	}
}

func TestAutoscaleScaleDown(t *testing.T) {
	cfg := &AutoscaleConfig{
		MinConcurrency:     2,
		MaxConcurrency:     20,
		ScaleUpThreshold:   0.3,
		ScaleDownThreshold: 0.6, // Lower threshold so combined score (utilization*0.7 + mem*0.3) can exceed it
		CooldownPeriod:     0,
	}

	pool := NewAutoscaledPool(cfg, func() int { return 0 }, testAutoscaleLogger)
	pool.current.Store(10) // pretend we're at 10 workers

	// Simulate very high utilization: all 10 active, 0 idle → utilization = 1.0
	pool.SetWorkerCounts(10, 0)

	decision := pool.Evaluate()
	if decision.Action != ScaleDown {
		t.Errorf("expected ScaleDown, got %v (loadScore=%.2f, utilization=%.2f)", decision.Action, decision.LoadScore, decision.Utilization)
	}
}

func TestAutoscaleNoAction(t *testing.T) {
	cfg := &AutoscaleConfig{
		MinConcurrency:     2,
		MaxConcurrency:     20,
		ScaleUpThreshold:   0.4,
		ScaleDownThreshold: 0.9,
		CooldownPeriod:     0,
	}

	pool := NewAutoscaledPool(cfg, func() int { return 0 }, testAutoscaleLogger)

	// Moderate utilization, empty queue
	pool.SetWorkerCounts(3, 3)

	decision := pool.Evaluate()
	// With empty queue, shouldn't scale up even with low utilization
	if decision.Action == ScaleUp {
		t.Errorf("should not scale up with empty queue")
	}
}

func TestAutoscaleRespectsMaxConcurrency(t *testing.T) {
	cfg := &AutoscaleConfig{
		MinConcurrency:     2,
		MaxConcurrency:     5,
		ScaleUpThreshold:   0.8,
		ScaleDownThreshold: 0.95,
		CooldownPeriod:     0,
	}

	pool := NewAutoscaledPool(cfg, func() int { return 1000 }, testAutoscaleLogger)
	pool.current.Store(5) // already at max

	pool.SetWorkerCounts(1, 4) // low utilization

	decision := pool.Evaluate()
	if decision.Desired > 5 {
		t.Errorf("should not exceed max concurrency 5, got %d", decision.Desired)
	}
}

func TestAutoscaleRespectsMinConcurrency(t *testing.T) {
	cfg := &AutoscaleConfig{
		MinConcurrency:     3,
		MaxConcurrency:     20,
		ScaleUpThreshold:   0.3,
		ScaleDownThreshold: 0.85,
		CooldownPeriod:     0,
	}

	pool := NewAutoscaledPool(cfg, func() int { return 0 }, testAutoscaleLogger)
	pool.current.Store(3) // already at min

	pool.SetWorkerCounts(3, 0) // high utilization

	decision := pool.Evaluate()
	if decision.Desired < 3 {
		t.Errorf("should not go below min concurrency 3, got %d", decision.Desired)
	}
}

func TestScaleStep(t *testing.T) {
	tests := []struct {
		current  int
		expected int
	}{
		{1, 1},
		{4, 1},
		{5, 2},
		{19, 2},
		{20, 5},
		{49, 5},
		{50, 10},
		{100, 10},
	}

	for _, tt := range tests {
		got := scaleStep(tt.current)
		if got != tt.expected {
			t.Errorf("scaleStep(%d) = %d, want %d", tt.current, got, tt.expected)
		}
	}
}

func TestWorkerCounts(t *testing.T) {
	cfg := DefaultAutoscaleConfig()
	pool := NewAutoscaledPool(cfg, func() int { return 0 }, testAutoscaleLogger)

	pool.SetWorkerCounts(5, 3)
	pool.RecordWorkerActive()
	// active should be 6, idle should be 2

	pool.RecordWorkerIdle()
	// back to 5 active, 3 idle
}

func BenchmarkAutoscaleEvaluate(b *testing.B) {
	cfg := &AutoscaleConfig{
		MinConcurrency:     2,
		MaxConcurrency:     100,
		ScaleUpThreshold:   0.5,
		ScaleDownThreshold: 0.9,
		CooldownPeriod:     0,
	}

	pool := NewAutoscaledPool(cfg, func() int { return 50 }, testAutoscaleLogger)
	pool.SetWorkerCounts(5, 5)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pool.Evaluate()
	}
}

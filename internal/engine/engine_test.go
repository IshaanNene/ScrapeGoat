package engine

import (
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// --- Frontier Tests ---

func TestFrontierPushPop(t *testing.T) {
	f := NewFrontier()

	r1, _ := types.NewRequest("https://example.com/page1")
	r1.Priority = 5
	r2, _ := types.NewRequest("https://example.com/page2")
	r2.Priority = 10

	f.Push(r1)
	f.Push(r2)

	if f.Len() != 2 {
		t.Fatalf("expected 2 items, got %d", f.Len())
	}

	// Lower priority value = higher priority (min-heap)
	got := f.TryPop()
	if got == nil {
		t.Fatal("expected non-nil, got nil")
	}
	t.Logf("First pop: priority %d, URL %s", got.Priority, got.URLString())

	got2 := f.TryPop()
	t.Logf("Second pop: priority %d, URL %s", got2.Priority, got2.URLString())

	// Verify both items came out
	if f.Len() != 0 {
		t.Errorf("expected empty frontier, got %d", f.Len())
	}
}

func TestFrontierTryPopEmpty(t *testing.T) {
	f := NewFrontier()
	got := f.TryPop()
	if got != nil {
		t.Errorf("expected nil from empty frontier, got %v", got)
	}
}

func TestFrontierClose(t *testing.T) {
	f := NewFrontier()
	f.Close()

	if !f.IsClosed() {
		t.Error("expected frontier to be closed")
	}
}

func TestFrontierMultipleItems(t *testing.T) {
	f := NewFrontier()

	for i := 0; i < 100; i++ {
		r, _ := types.NewRequest("https://example.com/page")
		r.Priority = i
		f.Push(r)
	}

	if f.Len() != 100 {
		t.Fatalf("expected 100 items, got %d", f.Len())
	}

	prev := -1
	for i := 0; i < 100; i++ {
		got := f.TryPop()
		if got == nil {
			t.Fatalf("unexpected nil at position %d", i)
		}
		if prev >= 0 && got.Priority < prev {
			t.Errorf("min-heap violation: got priority %d, previous was %d", got.Priority, prev)
		}
		prev = got.Priority
	}
}

// --- Deduplicator Tests ---

func TestDeduplicator(t *testing.T) {
	d := NewDeduplicator(1000)

	if d.IsSeen("https://example.com") {
		t.Error("should not be seen before marking")
	}

	d.MarkSeen("https://example.com")

	if !d.IsSeen("https://example.com") {
		t.Error("should be seen after marking")
	}
}

func TestDeduplicatorURLVariants(t *testing.T) {
	d := NewDeduplicator(1000)

	d.MarkSeen("https://Example.COM/Path?b=2&a=1")

	// Hostname case
	if !d.IsSeen("https://example.com/Path?b=2&a=1") {
		t.Error("hostname should be case-insensitive")
	}

	// Query param order
	if !d.IsSeen("https://example.com/Path?a=1&b=2") {
		t.Error("query params should be order-insensitive")
	}
}

// --- Stats Tests ---

func TestStatsSnapshot(t *testing.T) {
	s := &Stats{
		StartTime:   time.Now(),
		domainStats: make(map[string]*DomainStats),
	}
	s.RequestsSent.Add(42)
	s.ResponsesOK.Add(40)
	s.RequestsFailed.Add(2)
	s.BytesDownloaded.Add(1024 * 1024)

	snap := s.Snapshot()
	if snap["requests_sent"].(int64) != 42 {
		t.Errorf("expected 42 requests_sent, got %v", snap["requests_sent"])
	}
	if snap["bytes_downloaded"].(int64) != 1048576 {
		t.Errorf("expected 1048576 bytes, got %v", snap["bytes_downloaded"])
	}
}

// --- Benchmarks ---

func BenchmarkFrontierPushPop(b *testing.B) {
	f := NewFrontier()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := types.NewRequest("https://example.com/page")
		req.Priority = i % 10
		f.Push(req)
	}
	for i := 0; i < b.N; i++ {
		f.TryPop()
	}
}

func BenchmarkDeduplicator(b *testing.B) {
	d := NewDeduplicator(1_000_000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		url := "https://example.com/page/" + string(rune(i%26+'a'))
		d.MarkSeen(url)
	}

	b.Run("lookup", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			url := "https://example.com/page/" + string(rune(i%26+'a'))
			d.IsSeen(url)
		}
	})
}

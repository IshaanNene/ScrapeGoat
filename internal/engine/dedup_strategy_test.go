package engine

import (
	"fmt"
	"sync"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
)

func TestDedupStrategySelection(t *testing.T) {
	tests := []struct {
		strategy string
		wantType string
	}{
		{"", "*engine.Deduplicator"},      // default
		{"exact", "*engine.Deduplicator"}, // explicit
		{"EXACT", "*engine.Deduplicator"}, // case insensitive
		{"bloom", "*engine.BloomDeduplicator"},
		{"BLOOM", "*engine.BloomDeduplicator"},
		{"nonsense", "*engine.Deduplicator"}, // unknown falls back to the safe one
	}

	for _, tt := range tests {
		t.Run("strategy="+tt.strategy, func(t *testing.T) {
			cfg := testutil.LoopbackConfig()
			cfg.Engine.DedupStrategy = tt.strategy

			eng := New(cfg, concurrencyLogger)
			if got := fmt.Sprintf("%T", eng.dedup); got != tt.wantType {
				t.Errorf("dedup type = %s, want %s", got, tt.wantType)
			}
		})
	}
}

// TestBloomDedupIsMemoryBounded is the property the previous implementation did
// not have. It kept a Bloom filter *and* a complete exact set, so it used more
// memory than the plain map it was meant to improve on — the advertised 10-100x
// saving was never real.
func TestBloomDedupIsMemoryBounded(t *testing.T) {
	const urls = 200_000

	bd := NewBloomDeduplicator(urls, 0.01)
	for i := 0; i < urls; i++ {
		bd.MarkIfUnseen(fmt.Sprintf("https://example.com/page/%d", i))
	}

	stats := bd.MemoryStats()
	bytes := stats["bloom_memory_bytes"].(uint64)

	// A map of 32-byte hex hashes plus map overhead runs to roughly 40 bytes per
	// URL. The filter must be a small fraction of that or it is not buying
	// anything for the losses it introduces.
	exactApprox := uint64(urls) * 40
	if bytes >= exactApprox/4 {
		t.Errorf("bloom used %d bytes for %d URLs; an exact set is about %d, so this is "+
			"not a memory win", bytes, urls, exactApprox)
	}
	t.Logf("bloom: %d bytes for %d URLs (%.2f bytes/URL); exact would be ~%d",
		bytes, urls, float64(bytes)/float64(urls), exactApprox)
}

// TestBloomDedupHasNoFalseNegatives pins the guarantee that makes the trade
// acceptable: a URL genuinely crawled is never crawled again.
func TestBloomDedupHasNoFalseNegatives(t *testing.T) {
	const urls = 50_000

	bd := NewBloomDeduplicator(urls, 0.01)
	for i := 0; i < urls; i++ {
		bd.MarkIfUnseen(fmt.Sprintf("https://example.com/page/%d", i))
	}

	for i := 0; i < urls; i++ {
		u := fmt.Sprintf("https://example.com/page/%d", i)
		if bd.MarkIfUnseen(u) {
			t.Fatalf("false negative: %q was claimed but MarkIfUnseen granted it again", u)
		}
	}
}

// TestBloomDedupFalsePositiveRateIsHonest checks the cost side of the trade
// against the configured rate, so the documentation is not aspirational.
func TestBloomDedupFalsePositiveRateIsHonest(t *testing.T) {
	const (
		inserted = 100_000
		probes   = 100_000
		target   = 0.01
	)

	bd := NewBloomDeduplicator(inserted, target)
	for i := 0; i < inserted; i++ {
		bd.MarkIfUnseen(fmt.Sprintf("https://example.com/page/%d", i))
	}

	falsePositives := 0
	for i := 0; i < probes; i++ {
		if bd.IsSeen(fmt.Sprintf("https://other.test/never-seen/%d", i)) {
			falsePositives++
		}
	}

	rate := float64(falsePositives) / probes
	t.Logf("false-positive rate: %.4f (target %.4f) — %d URLs would be silently skipped",
		rate, target, falsePositives)

	// Allow headroom for hash luck, but the rate must be in the right order of
	// magnitude or the documented cost is wrong.
	if rate > target*2 {
		t.Errorf("false-positive rate %.4f is more than double the configured %.4f", rate, target)
	}
}

func TestBloomDedupIsConcurrencySafe(t *testing.T) {
	const goroutines = 32

	bd := NewBloomDeduplicator(1000, 0.01)

	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if bd.MarkIfUnseen("https://example.com/contested") {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Errorf("%d goroutines claimed the same URL, want exactly 1", winners)
	}
}

// TestBloomDedupCheckpointIsANoOp documents the limitation rather than leaving it
// to be discovered: a Bloom filter cannot enumerate its members, so state cannot
// round-trip through a checkpoint.
func TestBloomDedupCheckpointIsANoOp(t *testing.T) {
	bd := NewBloomDeduplicator(1000, 0.01)
	bd.MarkIfUnseen("https://example.com/a")

	if exported := bd.Export(); exported != nil {
		t.Errorf("Export returned %d entries; a Bloom filter cannot enumerate members", len(exported))
	}

	// Import must not panic or corrupt the filter.
	bd.Import([]string{"deadbeef", "cafebabe"})
	if !bd.IsSeen("https://example.com/a") {
		t.Error("Import disturbed existing filter state")
	}
}

func BenchmarkDedupExact(b *testing.B) {
	d := NewDeduplicator(b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.MarkIfUnseen(fmt.Sprintf("https://example.com/page/%d", i))
	}
}

func BenchmarkDedupBloom(b *testing.B) {
	d := NewBloomDeduplicator(b.N, 0.01)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.MarkIfUnseen(fmt.Sprintf("https://example.com/page/%d", i))
	}
}

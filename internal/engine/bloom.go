package engine

import (
	"hash"
	"hash/fnv"
	"math"
	"sync"
)

// BloomFilter is a space-efficient probabilistic data structure for URL deduplication.
// At scale (millions of URLs), a Bloom filter uses ~10 bits per element instead of
// storing full URL hashes, reducing memory by 10-100x while maintaining <1% false positive rate.
type BloomFilter struct {
	bits    []uint64
	numBits uint64
	numHash int
	mu      sync.RWMutex
	count   uint64

	// Retained so Reset can rebuild an identically-sized filter.
	expectedElements int
	fpRate           float64
}

// NewBloomFilter creates a new Bloom filter for the expected number of elements
// with the desired false positive rate (e.g., 0.01 = 1%).
//
// Example: 1M URLs at 1% FP rate → ~1.2 MB memory (vs ~64 MB for map-based dedup)
func NewBloomFilter(expectedElements int, fpRate float64) *BloomFilter {
	if expectedElements <= 0 {
		expectedElements = 100000
	}
	if fpRate <= 0 || fpRate >= 1 {
		fpRate = 0.01
	}

	// m = -(n * ln(p)) / (ln(2))^2
	n := float64(expectedElements)
	m := math.Ceil(-(n * math.Log(fpRate)) / (math.Log(2) * math.Log(2)))

	// k = (m/n) * ln(2)
	k := math.Ceil((m / n) * math.Log(2))

	numBits := uint64(m)
	// Round up to multiple of 64 for efficient storage
	numWords := (numBits + 63) / 64

	return &BloomFilter{
		expectedElements: expectedElements,
		fpRate:           fpRate,
		bits:             make([]uint64, numWords),
		numBits:          numBits,
		numHash:          int(k),
	}
}

// Add inserts a URL into the Bloom filter.
func (bf *BloomFilter) Add(url string) {
	bf.mu.Lock()
	defer bf.mu.Unlock()

	h1, h2 := bf.hashes(url)
	for i := 0; i < bf.numHash; i++ {
		pos := (h1 + uint64(i)*h2) % bf.numBits // nolint:gosec // Safe integer cast
		bf.bits[pos/64] |= 1 << (pos % 64)
	}
	bf.count++
}

// Contains checks if a URL might be in the set.
// Returns true if the URL is PROBABLY in the set (with false positive rate).
// Returns false if the URL is DEFINITELY NOT in the set.
func (bf *BloomFilter) Contains(url string) bool {
	bf.mu.RLock()
	defer bf.mu.RUnlock()

	h1, h2 := bf.hashes(url)
	for i := 0; i < bf.numHash; i++ {
		pos := (h1 + uint64(i)*h2) % bf.numBits // nolint:gosec // Safe integer cast
		if bf.bits[pos/64]&(1<<(pos%64)) == 0 {
			return false
		}
	}
	return true
}

// Count returns the number of elements added (not unique, since we can't know exactly).
func (bf *BloomFilter) Count() uint64 {
	bf.mu.RLock()
	defer bf.mu.RUnlock()
	return bf.count
}

// EstimatedFPRate returns the current estimated false positive rate.
func (bf *BloomFilter) EstimatedFPRate() float64 {
	bf.mu.RLock()
	defer bf.mu.RUnlock()

	setBits := uint64(0)
	for _, word := range bf.bits {
		setBits += popcount(word)
	}

	filledRatio := float64(setBits) / float64(bf.numBits)
	return math.Pow(filledRatio, float64(bf.numHash))
}

// MemoryUsageBytes returns the approximate memory usage in bytes.
func (bf *BloomFilter) MemoryUsageBytes() uint64 {
	return uint64(len(bf.bits)) * 8
}

// Reset clears the Bloom filter.
func (bf *BloomFilter) Reset() {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	for i := range bf.bits {
		bf.bits[i] = 0
	}
	bf.count = 0
}

// hashes returns two independent hash values for double hashing.
func (bf *BloomFilter) hashes(url string) (uint64, uint64) {
	h1 := fnvHash(url, 0)
	h2 := fnvHash(url, h1)
	return h1, h2
}

// fnvHash computes an FNV-1a hash with a seed.
func fnvHash(s string, seed uint64) uint64 {
	var h hash.Hash64
	if seed == 0 {
		h = fnv.New64a()
	} else {
		h = fnv.New64()
	}
	h.Write([]byte(s))
	return h.Sum64() ^ seed
}

// popcount counts the number of set bits in a uint64.
func popcount(x uint64) uint64 {
	// Hamming weight (Brian Kernighan's algorithm)
	count := uint64(0)
	for x != 0 {
		x &= x - 1
		count++
	}
	return count
}

// Deduper is the deduplication strategy the engine uses to decide whether a URL
// has already been claimed.
//
// Two implementations, with genuinely different trades:
//
//   - Deduplicator keeps every URL hash in a map. Exact, and costs roughly 40
//     bytes per URL, so a billion-URL crawl needs tens of gigabytes.
//   - BloomDeduplicator keeps a Bloom filter and nothing else. Roughly 1.8 bytes
//     per URL at a 1% false-positive rate, and *lossy*: a false positive means a
//     URL is treated as already-seen and never crawled.
type Deduper interface {
	// MarkIfUnseen atomically claims a URL, reporting whether it was new.
	MarkIfUnseen(rawURL string) bool

	// IsSeen reports whether a URL has been claimed.
	IsSeen(rawURL string) bool

	// Count returns how many unique URLs have been claimed.
	Count() int

	// Export and Import carry state across a checkpoint.
	Export() []string
	Import(hashes []string)

	// Reset clears all state.
	Reset()
}

// BloomDeduplicator is a memory-bounded, lossy deduplicator.
//
// It holds a Bloom filter and nothing else. That is the point: the earlier version
// of this type kept a Bloom filter *and* a complete exact set, so it used strictly
// more memory than the plain map it was meant to improve on — the fast negative
// lookup was real, the advertised 10-100x memory saving was not.
//
// # The trade
//
// Bloom filters have no false negatives and some false positives. For crawl dedup
// that means: a URL genuinely seen is always reported seen (never re-crawled), and
// a URL never seen is occasionally reported seen — and therefore silently skipped.
// At the default 1% rate, roughly one URL in a hundred is dropped.
//
// That is a real cost and it is why this is not the default. It is the right trade
// only when the crawl is large enough that an exact set will not fit, where the
// alternative is not "crawl everything" but "run out of memory".
//
// Export and Import are deliberately unsupported: a Bloom filter cannot enumerate
// its members, so a checkpoint cannot round-trip through one. Resuming a Bloom
// crawl restarts with an empty filter, which re-crawls rather than drops — the
// safer direction of the two.
type BloomDeduplicator struct {
	mu    sync.Mutex
	bloom *BloomFilter
}

// NewBloomDeduplicator creates a Bloom-backed deduplicator sized for the expected
// number of URLs, at the given false-positive rate. A rate of zero uses 1%.
func NewBloomDeduplicator(expectedElements int, fpRate float64) *BloomDeduplicator {
	if fpRate <= 0 || fpRate >= 1 {
		fpRate = 0.01
	}
	return &BloomDeduplicator{bloom: NewBloomFilter(expectedElements, fpRate)}
}

// MarkIfUnseen atomically claims a URL, reporting whether it was new.
func (bd *BloomDeduplicator) MarkIfUnseen(rawURL string) bool {
	canonical := CanonicalizeURL(rawURL)

	bd.mu.Lock()
	defer bd.mu.Unlock()

	if bd.bloom.Contains(canonical) {
		return false
	}
	bd.bloom.Add(canonical)
	return true
}

// IsSeen reports whether a URL has been claimed, subject to the filter's
// false-positive rate.
func (bd *BloomDeduplicator) IsSeen(rawURL string) bool {
	canonical := CanonicalizeURL(rawURL)

	bd.mu.Lock()
	defer bd.mu.Unlock()
	return bd.bloom.Contains(canonical)
}

// Count returns the number of URLs added.
func (bd *BloomDeduplicator) Count() int {
	bd.mu.Lock()
	defer bd.mu.Unlock()
	return int(bd.bloom.Count())
}

// Export returns nil: a Bloom filter cannot enumerate its members, so there is
// nothing to checkpoint. See the type comment.
func (bd *BloomDeduplicator) Export() []string { return nil }

// Import is a no-op, for the same reason as Export.
func (bd *BloomDeduplicator) Import([]string) {}

// Reset clears the filter.
func (bd *BloomDeduplicator) Reset() {
	bd.mu.Lock()
	defer bd.mu.Unlock()
	bd.bloom = NewBloomFilter(bd.bloom.expectedElements, bd.bloom.fpRate)
}

// MemoryStats returns memory usage information.
func (bd *BloomDeduplicator) MemoryStats() map[string]any {
	bd.mu.Lock()
	defer bd.mu.Unlock()
	return map[string]any{
		"bloom_memory_bytes": bd.bloom.MemoryUsageBytes(),
		"bloom_fp_rate":      bd.bloom.EstimatedFPRate(),
		"bloom_count":        bd.bloom.Count(),
	}
}

// Compile-time confirmation that both strategies satisfy the interface.
var (
	_ Deduper = (*Deduplicator)(nil)
	_ Deduper = (*BloomDeduplicator)(nil)
)

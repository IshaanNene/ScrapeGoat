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
		bits:    make([]uint64, numWords),
		numBits: numBits,
		numHash: int(k),
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

// BloomDeduplicator combines Bloom filter with exact dedup for best of both worlds.
// Uses Bloom filter for fast negative lookups, falls back to exact hash for positives.
type BloomDeduplicator struct {
	bloom *BloomFilter
	exact *Deduplicator
}

// NewBloomDeduplicator creates a hybrid deduplicator.
func NewBloomDeduplicator(expectedElements int) *BloomDeduplicator {
	return &BloomDeduplicator{
		bloom: NewBloomFilter(expectedElements, 0.001), // 0.1% FP rate
		exact: NewDeduplicator(expectedElements),
	}
}

// IsSeen checks if a URL has been seen (fast path via Bloom filter).
func (bd *BloomDeduplicator) IsSeen(rawURL string) bool {
	canonical := CanonicalizeURL(rawURL)
	// Fast negative check via Bloom filter
	if !bd.bloom.Contains(canonical) {
		return false
	}
	// Verify with exact dedup (handles false positives)
	return bd.exact.IsSeen(rawURL)
}

// MarkSeen marks a URL as seen in both structures.
func (bd *BloomDeduplicator) MarkSeen(rawURL string) {
	canonical := CanonicalizeURL(rawURL)
	bd.bloom.Add(canonical)
	bd.exact.MarkSeen(rawURL)
}

// Count returns the number of unique URLs seen.
func (bd *BloomDeduplicator) Count() int {
	return bd.exact.Count()
}

// MemoryStats returns memory usage information.
func (bd *BloomDeduplicator) MemoryStats() map[string]any {
	return map[string]any{
		"bloom_memory_bytes": bd.bloom.MemoryUsageBytes(),
		"bloom_fp_rate":      bd.bloom.EstimatedFPRate(),
		"bloom_count":        bd.bloom.Count(),
		"exact_count":        bd.exact.Count(),
	}
}

package engine

import (
	"fmt"
	"testing"
)

func TestBloomFilter(t *testing.T) {
	tests := []struct {
		name      string
		elements  int
		fpRate    float64
		addURLs   []string
		checkURLs []string
		wantFound []bool
	}{
		{
			name: "basic add and contains",
			elements: 1000, fpRate: 0.01,
			addURLs:   []string{"https://a.com", "https://b.com"},
			checkURLs: []string{"https://a.com", "https://b.com", "https://c.com"},
			wantFound: []bool{true, true, false},
		},
		{
			name: "empty filter returns false",
			elements: 100, fpRate: 0.01,
			addURLs:   nil,
			checkURLs: []string{"https://anything.com"},
			wantFound: []bool{false},
		},
		{
			name: "default params for invalid inputs",
			elements: -1, fpRate: 2.0,
			addURLs:   []string{"https://x.com"},
			checkURLs: []string{"https://x.com"},
			wantFound: []bool{true},
		},
		{
			name: "many URLs no false negatives",
			elements: 10000, fpRate: 0.01,
			addURLs: func() []string {
				urls := make([]string, 500)
				for i := range urls {
					urls[i] = fmt.Sprintf("https://example.com/page/%d", i)
				}
				return urls
			}(),
			checkURLs: func() []string {
				urls := make([]string, 500)
				for i := range urls {
					urls[i] = fmt.Sprintf("https://example.com/page/%d", i)
				}
				return urls
			}(),
			wantFound: func() []bool {
				b := make([]bool, 500)
				for i := range b {
					b[i] = true
				}
				return b
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bf := NewBloomFilter(tt.elements, tt.fpRate)
			for _, u := range tt.addURLs {
				bf.Add(u)
			}
			if got := bf.Count(); got != uint64(len(tt.addURLs)) {
				t.Errorf("Count() = %d, want %d", got, len(tt.addURLs))
			}
			for i, u := range tt.checkURLs {
				if got := bf.Contains(u); got != tt.wantFound[i] {
					t.Errorf("Contains(%q) = %v, want %v", u, got, tt.wantFound[i])
				}
			}
			if bf.MemoryUsageBytes() == 0 {
				t.Error("MemoryUsageBytes() should be > 0")
			}
		})
	}
}

func TestBloomFilterFPRate(t *testing.T) {
	bf := NewBloomFilter(10000, 0.01)
	for i := 0; i < 10000; i++ {
		bf.Add(fmt.Sprintf("https://example.com/%d", i))
	}
	fpRate := bf.EstimatedFPRate()
	if fpRate > 0.05 {
		t.Errorf("FP rate %.4f exceeds 5%% threshold", fpRate)
	}
	t.Logf("Estimated FP rate: %.6f (memory: %d bytes)", fpRate, bf.MemoryUsageBytes())
}

func TestBloomFilterReset(t *testing.T) {
	bf := NewBloomFilter(100, 0.01)
	bf.Add("https://example.com")

	if !bf.Contains("https://example.com") {
		t.Fatal("should contain URL after Add")
	}

	bf.Reset()

	if bf.Contains("https://example.com") {
		t.Error("should not contain URL after Reset")
	}
	if bf.Count() != 0 {
		t.Errorf("Count after Reset = %d, want 0", bf.Count())
	}
}

func TestBloomDeduplicator(t *testing.T) {
	bd := NewBloomDeduplicator(10000)

	if bd.IsSeen("https://example.com") {
		t.Error("should not be seen before marking")
	}

	bd.MarkSeen("https://example.com")

	if !bd.IsSeen("https://example.com") {
		t.Error("should be seen after marking")
	}

	if bd.Count() != 1 {
		t.Errorf("Count = %d, want 1", bd.Count())
	}

	stats := bd.MemoryStats()
	if stats["bloom_memory_bytes"].(uint64) == 0 {
		t.Error("bloom memory should be > 0")
	}
}

package engine

import (
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

func TestCheckpointSaveLoad(t *testing.T) {
	tests := []struct {
		name     string
		urls     []string
		depths   []int
		statsReq int64
	}{
		{
			name:     "single URL",
			urls:     []string{"https://example.com"},
			depths:   []int{0},
			statsReq: 5,
		},
		{
			name:     "multiple URLs with depths",
			urls:     []string{"https://a.com", "https://b.com", "https://c.com"},
			depths:   []int{0, 1, 2},
			statsReq: 42,
		},
		{
			name:     "empty frontier",
			urls:     nil,
			depths:   nil,
			statsReq: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewCheckpointManager(time.Minute)
			cm.checkpointDir = t.TempDir()

			frontier := NewFrontier()
			dedup := NewDeduplicator(1000)
			stats := &Stats{domainStats: make(map[string]*DomainStats)}
			stats.RequestsSent.Store(tt.statsReq)

			for i, u := range tt.urls {
				req, err := types.NewRequest(u)
				if err != nil {
					t.Fatalf("NewRequest(%q): %v", u, err)
				}
				req.Depth = tt.depths[i]
				frontier.Push(req)
				dedup.MarkSeen(u)
			}

			if err := cm.Save(frontier, dedup, stats); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if !cm.HasCheckpoint() {
				t.Fatal("checkpoint should exist after Save")
			}

			// Restore into fresh instances
			f2 := NewFrontier()
			d2 := NewDeduplicator(1000)
			s2 := &Stats{domainStats: make(map[string]*DomainStats)}

			if err := cm.Load(f2, d2, s2); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if f2.Len() != len(tt.urls) {
				t.Errorf("restored frontier len=%d, want %d", f2.Len(), len(tt.urls))
			}
			if s2.RequestsSent.Load() != tt.statsReq {
				t.Errorf("restored stats.RequestsSent=%d, want %d", s2.RequestsSent.Load(), tt.statsReq)
			}

			// Verify dedup state was restored
			for _, u := range tt.urls {
				if !d2.IsSeen(u) {
					t.Errorf("URL %q should be seen after restore", u)
				}
			}

			if err := cm.Clean(); err != nil {
				t.Fatalf("Clean: %v", err)
			}
			if cm.HasCheckpoint() {
				t.Error("checkpoint should not exist after Clean")
			}
		})
	}
}

func TestCheckpointLoadNonExistent(t *testing.T) {
	cm := NewCheckpointManager(time.Minute)
	cm.checkpointDir = t.TempDir()

	f := NewFrontier()
	d := NewDeduplicator(100)
	s := &Stats{domainStats: make(map[string]*DomainStats)}

	if err := cm.Load(f, d, s); err != nil {
		t.Errorf("Load on nonexistent should return nil, got %v", err)
	}

	if f.Len() != 0 {
		t.Errorf("frontier should be empty after loading from nonexistent checkpoint")
	}
}

func TestCheckpointOverwrite(t *testing.T) {
	cm := NewCheckpointManager(time.Minute)
	cm.checkpointDir = t.TempDir()

	frontier := NewFrontier()
	dedup := NewDeduplicator(100)
	stats := &Stats{domainStats: make(map[string]*DomainStats)}

	// Save first checkpoint
	req, _ := types.NewRequest("https://first.com")
	frontier.Push(req)
	stats.RequestsSent.Store(10)
	cm.Save(frontier, dedup, stats)

	// Save second checkpoint (overwrites)
	req2, _ := types.NewRequest("https://second.com")
	frontier.Push(req2)
	stats.RequestsSent.Store(20)
	cm.Save(frontier, dedup, stats)

	// Load should reflect second save
	f2 := NewFrontier()
	d2 := NewDeduplicator(100)
	s2 := &Stats{domainStats: make(map[string]*DomainStats)}
	cm.Load(f2, d2, s2)

	if s2.RequestsSent.Load() != 20 {
		t.Errorf("RequestsSent=%d, want 20 (from second save)", s2.RequestsSent.Load())
	}
}

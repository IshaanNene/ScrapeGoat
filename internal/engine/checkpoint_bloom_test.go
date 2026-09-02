package engine

import (
	"strings"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
)

// TestCheckpointRefusesADeduperItCannotSerialise covers the other way a resume
// used to lose state.
//
// A Bloom filter cannot enumerate its members, so Export returns nil by design.
// The checkpoint wrote that out as an empty seen set, and resuming from it
// re-crawled every URL the previous run had covered — silently, and looking like
// a slow crawl rather than a broken one.
func TestCheckpointRefusesADeduperItCannotSerialise(t *testing.T) {
	checkpointInDir(t)

	cfg := testutil.LoopbackConfig()
	// Off, or AddSeed fetches robots.txt from the real example.com — a unit test
	// about checkpoint serialisation has no business making a network request,
	// and the idle connection it leaves fails goleak.
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.DedupStrategy = "bloom"
	cfg.Engine.ExpectedURLs = 1000

	eng := New(cfg, concurrencyLogger)
	if _, ok := eng.dedup.(*BloomDeduplicator); !ok {
		t.Fatalf("config asked for bloom dedup, got %T", eng.dedup)
	}
	if err := eng.AddSeed("https://example.com/seed"); err != nil {
		t.Fatalf("add seed: %v", err)
	}

	err := eng.checkpoint.Save(eng.frontier, eng.dedup, eng.stats)
	if err == nil {
		t.Fatal("checkpointing a bloom crawl was accepted; the resume would re-crawl everything")
	}
	if !strings.Contains(err.Error(), "re-crawl") {
		t.Errorf("error does not explain the consequence: %v", err)
	}
	if !eng.HasCheckpoint() {
		return // nothing written, which is the point
	}
	t.Error("a checkpoint file was written despite the seen set being unsaveable")
}

// TestCheckpointAllowsAnEmptyExactDeduper guards the boundary: a crawl that has
// genuinely claimed nothing yet must still checkpoint. Distinguishing "exported
// nothing because there is nothing" from "exported nothing because it cannot" is
// the whole of the check.
func TestCheckpointAllowsAnEmptyExactDeduper(t *testing.T) {
	checkpointInDir(t)

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	eng := New(cfg, concurrencyLogger)

	if got := eng.dedup.Count(); got != 0 {
		t.Fatalf("fresh deduper holds %d URLs, want 0", got)
	}
	if err := eng.checkpoint.Save(eng.frontier, eng.dedup, eng.stats); err != nil {
		t.Errorf("checkpointing a crawl that has claimed nothing failed: %v", err)
	}
}

package engine

import (
	"os"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// checkpointInDir points an engine's checkpoint manager at a scratch directory.
// CheckpointManager hardcodes a relative path, so the test chdirs rather than
// leaving .scrapegoat_checkpoints behind in the package directory.
func checkpointInDir(t *testing.T) {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

// TestResumeRestoresCrawlState is the test that could not exist while
// CheckpointManager.Load had no caller: a crawl's remaining work must survive a
// restart.
func TestResumeRestoresCrawlState(t *testing.T) {
	checkpointInDir(t)

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.MaxDepth = 5

	// First run: queue some work and record progress, then checkpoint.
	first := New(cfg, concurrencyLogger)
	for _, u := range []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	} {
		req, err := types.NewRequest(u)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if err := first.AddRequest(req); err != nil {
			t.Fatalf("add %s: %v", u, err)
		}
	}
	first.stats.RequestsSent.Store(42)
	first.stats.ItemsScraped.Store(7)

	if err := first.checkpoint.Save(first.frontier, first.dedup, first.stats); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	// Second run: a fresh engine, as a restarted process would have.
	second := New(cfg, concurrencyLogger)
	if !second.HasCheckpoint() {
		t.Fatal("HasCheckpoint reported no checkpoint after Save")
	}
	if err := second.ResumeFromCheckpoint(); err != nil {
		t.Fatalf("resume: %v", err)
	}

	if got := second.frontier.Len(); got != 3 {
		t.Errorf("restored frontier holds %d requests, want 3", got)
	}
	if got := second.stats.RequestsSent.Load(); got != 42 {
		t.Errorf("restored requests_sent = %d, want 42", got)
	}
	if got := second.stats.ItemsScraped.Load(); got != 7 {
		t.Errorf("restored items_scraped = %d, want 7", got)
	}
	if !second.dedup.IsSeen("https://example.com/a") {
		t.Error("restored dedup set does not contain a URL the first run had seen")
	}
}

// TestResumeSuppressesAlreadyCrawledSeeds is the behaviour that makes --resume
// useful rather than merely non-crashing: re-running the same command must not
// re-crawl what the previous run already covered.
func TestResumeSuppressesAlreadyCrawledSeeds(t *testing.T) {
	checkpointInDir(t)

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false

	first := New(cfg, concurrencyLogger)
	if err := first.AddSeed("https://example.com/seed"); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	// Simulate the seed having been crawled: take it from the frontier and finish
	// it, leaving it in the dedup set.
	//
	// Done is the load-bearing half. Dequeuing alone used to be indistinguishable
	// from completing, because a request in a worker's hands was tracked nowhere —
	// which is exactly the bug that lost in-flight work on every resume. Now that
	// the two states differ, a test about work that finished has to say so.
	req := first.frontier.TryPop()
	if req == nil {
		t.Fatal("seed was not queued")
	}
	first.frontier.Done(req)
	if err := first.checkpoint.Save(first.frontier, first.dedup, first.stats); err != nil {
		t.Fatalf("save: %v", err)
	}

	second := New(cfg, concurrencyLogger)
	if err := second.ResumeFromCheckpoint(); err != nil {
		t.Fatalf("resume: %v", err)
	}

	err := second.AddSeed("https://example.com/seed")
	if err == nil {
		t.Fatal("a seed already crawled by the previous run was accepted again")
	}
	if second.frontier.Len() != 0 {
		t.Errorf("frontier holds %d requests after re-seeding, want 0", second.frontier.Len())
	}
}

func TestResumeWithoutCheckpointIsAnError(t *testing.T) {
	checkpointInDir(t)

	eng := New(testutil.LoopbackConfig(), concurrencyLogger)
	if eng.HasCheckpoint() {
		t.Fatal("HasCheckpoint reported a checkpoint in an empty directory")
	}
	if err := eng.ResumeFromCheckpoint(); err == nil {
		t.Error("resuming with no checkpoint should be an error, not a silent fresh start")
	}
}

func TestClearCheckpoint(t *testing.T) {
	checkpointInDir(t)

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false

	eng := New(cfg, concurrencyLogger)
	if err := eng.AddSeed("https://example.com/x"); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	if err := eng.checkpoint.Save(eng.frontier, eng.dedup, eng.stats); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !eng.HasCheckpoint() {
		t.Fatal("checkpoint was not written")
	}

	if err := eng.ClearCheckpoint(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if eng.HasCheckpoint() {
		t.Error("checkpoint survived ClearCheckpoint; the next run would resume finished work")
	}
}

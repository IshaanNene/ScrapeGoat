package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// CheckpointManager handles saving and loading crawl state for pause/resume.
type CheckpointManager struct {
	interval      time.Duration
	checkpointDir string
}

// checkpointData is the serializable crawl state.
type checkpointData struct {
	Timestamp    time.Time       `json:"timestamp"`
	FrontierURLs []checkpointReq `json:"frontier_urls"`
	SeenHashes   []string        `json:"seen_hashes"`
	Stats        checkpointStats `json:"stats"`
}

type checkpointReq struct {
	URL       string `json:"url"`
	Depth     int    `json:"depth"`
	Priority  int    `json:"priority"`
	ParentURL string `json:"parent_url,omitempty"`
}

type checkpointStats struct {
	RequestsSent    int64 `json:"requests_sent"`
	RequestsFailed  int64 `json:"requests_failed"`
	ResponsesOK     int64 `json:"responses_ok"`
	ResponsesError  int64 `json:"responses_error"`
	ItemsScraped    int64 `json:"items_scraped"`
	URLsEnqueued    int64 `json:"urls_enqueued"`
	BytesDownloaded int64 `json:"bytes_downloaded"`
}

// NewCheckpointManager creates a new CheckpointManager.
func NewCheckpointManager(interval time.Duration) *CheckpointManager {
	return &CheckpointManager{
		interval:      interval,
		checkpointDir: ".scrapegoat_checkpoints",
	}
}

// Save serializes the current crawl state to disk.
func (cm *CheckpointManager) Save(frontier *Frontier, dedup *Deduplicator, stats *Stats) error {
	if err := os.MkdirAll(cm.checkpointDir, 0o755); err != nil {
		return fmt.Errorf("create checkpoint dir: %w", err)
	}

	// Snapshot frontier (non-destructive — items stay in queue)
	requests := frontier.Snapshot()

	data := checkpointData{
		Timestamp:    time.Now(),
		FrontierURLs: make([]checkpointReq, len(requests)),
		SeenHashes:   dedup.Export(),
		Stats: checkpointStats{
			RequestsSent:    stats.RequestsSent.Load(),
			RequestsFailed:  stats.RequestsFailed.Load(),
			ResponsesOK:     stats.ResponsesOK.Load(),
			ResponsesError:  stats.ResponsesError.Load(),
			ItemsScraped:    stats.ItemsScraped.Load(),
			URLsEnqueued:    stats.URLsEnqueued.Load(),
			BytesDownloaded: stats.BytesDownloaded.Load(),
		},
	}

	for i, req := range requests {
		data.FrontierURLs[i] = checkpointReq{
			URL:       req.URLString(),
			Depth:     req.Depth,
			Priority:  req.Priority,
			ParentURL: req.ParentURL,
		}
	}

	// Write to temp file, then rename (atomic write)
	tmpPath := filepath.Join(cm.checkpointDir, "checkpoint.tmp")
	finalPath := filepath.Join(cm.checkpointDir, "checkpoint.json")

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create checkpoint file: %w", err)
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		f.Close()
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	f.Close()

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename checkpoint file: %w", err)
	}

	return nil
}

// Load reads a checkpoint from disk and restores crawl state.
func (cm *CheckpointManager) Load(frontier *Frontier, dedup *Deduplicator, stats *Stats) error {
	path := filepath.Join(cm.checkpointDir, "checkpoint.json")

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No checkpoint to restore
		}
		return fmt.Errorf("open checkpoint: %w", err)
	}
	defer f.Close()

	var data checkpointData
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return fmt.Errorf("decode checkpoint: %w", err)
	}

	// Restore dedup state
	dedup.Import(data.SeenHashes)

	// Restore frontier
	for _, cr := range data.FrontierURLs {
		req, err := newRequestFromCheckpoint(cr)
		if err != nil {
			continue
		}
		frontier.Push(req)
	}

	// Restore stats
	stats.RequestsSent.Store(data.Stats.RequestsSent)
	stats.RequestsFailed.Store(data.Stats.RequestsFailed)
	stats.ResponsesOK.Store(data.Stats.ResponsesOK)
	stats.ResponsesError.Store(data.Stats.ResponsesError)
	stats.ItemsScraped.Store(data.Stats.ItemsScraped)
	stats.URLsEnqueued.Store(data.Stats.URLsEnqueued)
	stats.BytesDownloaded.Store(data.Stats.BytesDownloaded)

	return nil
}

// HasCheckpoint returns true if a checkpoint file exists.
func (cm *CheckpointManager) HasCheckpoint() bool {
	path := filepath.Join(cm.checkpointDir, "checkpoint.json")
	_, err := os.Stat(path)
	return err == nil
}

// Clean removes the checkpoint file.
func (cm *CheckpointManager) Clean() error {
	path := filepath.Join(cm.checkpointDir, "checkpoint.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func newRequestFromCheckpoint(cr checkpointReq) (*types.Request, error) {
	req, err := types.NewRequest(cr.URL)
	if err != nil {
		return nil, err
	}
	req.Depth = cr.Depth
	req.Priority = cr.Priority
	req.ParentURL = cr.ParentURL
	return req, nil
}

// Dir returns the directory checkpoints are written to.
func (cm *CheckpointManager) Dir() string { return cm.checkpointDir }

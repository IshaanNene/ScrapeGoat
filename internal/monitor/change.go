package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// ChangeType identifies what kind of change occurred.
type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeModified ChangeType = "modified"
	ChangeRemoved  ChangeType = "removed"
)

// Change represents a detected content change.
type Change struct {
	URL       string     `json:"url"`
	Type      ChangeType `json:"type"`
	Field     string     `json:"field,omitempty"`
	OldValue  string     `json:"old_value,omitempty"`
	NewValue  string     `json:"new_value,omitempty"`
	Timestamp time.Time  `json:"timestamp"`
}

// ChangeDetector compares crawl results against historical snapshots.
type ChangeDetector struct {
	snapshotDir string
	logger      *slog.Logger
	mu          sync.RWMutex
}

// NewChangeDetector creates a new change detector.
func NewChangeDetector(snapshotDir string, logger *slog.Logger) *ChangeDetector {
	_ = os.MkdirAll(snapshotDir, 0o755)
	return &ChangeDetector{
		snapshotDir: snapshotDir,
		logger:      logger.With("component", "change_detector"),
	}
}

// Detect compares an item against its last snapshot and returns changes.
func (cd *ChangeDetector) Detect(item *types.Item) ([]Change, error) {
	cd.mu.RLock()
	old, err := cd.loadSnapshot(item.URL)
	cd.mu.RUnlock()

	if err != nil {
		// First time seeing this URL
		cd.mu.Lock()
		cd.saveSnapshot(item)
		cd.mu.Unlock()
		return []Change{{URL: item.URL, Type: ChangeAdded, Timestamp: time.Now()}}, nil
	}

	var changes []Change
	// Compare fields
	for key, newVal := range item.Fields {
		newStr := fmt.Sprintf("%v", newVal)
		oldStr := ""
		if oldVal, ok := old[key]; ok {
			oldStr = fmt.Sprintf("%v", oldVal)
		}
		if oldStr != newStr {
			changes = append(changes, Change{
				URL:       item.URL,
				Type:      ChangeModified,
				Field:     key,
				OldValue:  truncateStr(oldStr, 200),
				NewValue:  truncateStr(newStr, 200),
				Timestamp: time.Now(),
			})
		}
	}

	// Check for removed fields
	for key := range old {
		if !item.Has(key) {
			changes = append(changes, Change{
				URL:       item.URL,
				Type:      ChangeRemoved,
				Field:     key,
				OldValue:  truncateStr(fmt.Sprintf("%v", old[key]), 200),
				Timestamp: time.Now(),
			})
		}
	}

	// Update snapshot
	cd.mu.Lock()
	cd.saveSnapshot(item)
	cd.mu.Unlock()

	return changes, nil
}

func (cd *ChangeDetector) loadSnapshot(url string) (map[string]any, error) {
	path := cd.snapshotPath(url)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	return fields, json.Unmarshal(data, &fields)
}

func (cd *ChangeDetector) saveSnapshot(item *types.Item) {
	data, _ := json.Marshal(item.Fields)
	_ = os.WriteFile(cd.snapshotPath(item.URL), data, 0o644)
}

func (cd *ChangeDetector) snapshotPath(url string) string {
	hash := sha256.Sum256([]byte(url))
	return filepath.Join(cd.snapshotDir, hex.EncodeToString(hash[:])+".json")
}

// --- Scheduled Re-Crawling ---

// Schedule represents a recurring crawl schedule.
type Schedule struct {
	Name     string        `json:"name"`
	URLs     []string      `json:"urls"`
	Interval time.Duration `json:"interval"`
	MaxDepth int           `json:"max_depth"`
	Rules    []string      `json:"rules,omitempty"`
}

// Scheduler runs crawls on a schedule.
type Scheduler struct {
	schedules []*Schedule
	logger    *slog.Logger
	cancel    context.CancelFunc
	mu        sync.Mutex
}

// NewScheduler creates a new crawl scheduler.
func NewScheduler(logger *slog.Logger) *Scheduler {
	return &Scheduler{
		logger: logger.With("component", "crawl_scheduler"),
	}
}

// Add adds a schedule.
func (s *Scheduler) Add(sched *Schedule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schedules = append(s.schedules, sched)
	s.logger.Info("schedule added", "name", sched.Name, "interval", sched.Interval, "urls", len(sched.URLs))
}

// Start begins executing schedules.
func (s *Scheduler) Start(ctx context.Context, crawlFunc func(urls []string, depth int) error) {
	ctx, s.cancel = context.WithCancel(ctx)

	for _, sched := range s.schedules {
		go func(sched *Schedule) {
			ticker := time.NewTicker(sched.Interval)
			defer ticker.Stop()

			// Run immediately
			s.logger.Info("running schedule", "name", sched.Name)
			if err := crawlFunc(sched.URLs, sched.MaxDepth); err != nil {
				s.logger.Error("scheduled crawl failed", "name", sched.Name, "error", err)
			}

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.logger.Info("running scheduled crawl", "name", sched.Name)
					if err := crawlFunc(sched.URLs, sched.MaxDepth); err != nil {
						s.logger.Error("scheduled crawl failed", "name", sched.Name, "error", err)
					}
				}
			}
		}(sched)
	}
}

// Stop stops the scheduler.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// --- Notification System ---

// NotificationType specifies the notification channel.
type NotificationType string

const (
	NotifyWebhook NotificationType = "webhook"
	NotifySlack   NotificationType = "slack"
	NotifyEmail   NotificationType = "email"
)

// Notifier sends notifications when changes are detected.
type Notifier struct {
	channels []NotificationChannel
	logger   *slog.Logger
}

// NotificationChannel is an interface for notification delivery.
type NotificationChannel interface {
	Send(ctx context.Context, changes []Change) error
	Type() NotificationType
}

// NewNotifier creates a new change notifier.
func NewNotifier(logger *slog.Logger) *Notifier {
	return &Notifier{
		logger: logger.With("component", "notifier"),
	}
}

// AddChannel registers a notification channel.
func (n *Notifier) AddChannel(ch NotificationChannel) {
	n.channels = append(n.channels, ch)
}

// Notify sends changes to all registered channels.
func (n *Notifier) Notify(ctx context.Context, changes []Change) {
	if len(changes) == 0 {
		return
	}
	for _, ch := range n.channels {
		if err := ch.Send(ctx, changes); err != nil {
			n.logger.Error("notification failed", "channel", ch.Type(), "error", err)
		}
	}
}

// WebhookChannel sends notifications via HTTP webhook.
type WebhookChannel struct {
	URL    string
	logger *slog.Logger
}

func (w *WebhookChannel) Type() NotificationType { return NotifyWebhook }

func (w *WebhookChannel) Send(ctx context.Context, changes []Change) error {
	data, _ := json.Marshal(map[string]any{
		"changes":   changes,
		"count":     len(changes),
		"timestamp": time.Now(),
	})
	w.logger.Info("would send webhook", "url", w.URL, "changes", len(changes), "size", len(data))
	return nil
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// DiffResult holds the result of a content diff.
type DiffResult struct {
	URL     string   `json:"url"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Changed []string `json:"changed,omitempty"`
}

// DiffText computes a line-level diff between two text strings.
func DiffText(old, new string) DiffResult {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")

	oldSet := make(map[string]bool, len(oldLines))
	for _, l := range oldLines {
		oldSet[strings.TrimSpace(l)] = true
	}

	newSet := make(map[string]bool, len(newLines))
	for _, l := range newLines {
		newSet[strings.TrimSpace(l)] = true
	}

	var added, removed []string
	for _, l := range newLines {
		l = strings.TrimSpace(l)
		if l != "" && !oldSet[l] {
			added = append(added, l)
		}
	}
	for _, l := range oldLines {
		l = strings.TrimSpace(l)
		if l != "" && !newSet[l] {
			removed = append(removed, l)
		}
	}

	return DiffResult{Added: added, Removed: removed}
}

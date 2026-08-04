package apiserver

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Job status constants.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusPaused    = "paused"
)

// Job priority constants.
const (
	PriorityLow    = 0
	PriorityNormal = 1
	PriorityHigh   = 2
)

// Job represents a crawl/extract job.
type Job struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"` // crawl, extract, batch
	URL       string         `json:"url"`
	Status    string         `json:"status"`
	Priority  int            `json:"priority"`
	ItemCount int            `json:"item_count"`
	Error     string         `json:"error,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
	Stats     map[string]any `json:"stats,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	StartedAt *time.Time     `json:"started_at,omitempty"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
}

// JobConfig is the input for creating a new job.
type JobConfig struct {
	Type     string
	URL      string
	Priority int
	Config   map[string]any
}

// JobManager persists jobs and their items in SQLite.
type JobManager struct {
	db     *sql.DB
	logger *slog.Logger
	mu     sync.RWMutex
}

// NewJobManager creates or opens the job database.
func NewJobManager(dbPath string, logger *slog.Logger) (*JobManager, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id         TEXT PRIMARY KEY,
			type       TEXT NOT NULL,
			url        TEXT NOT NULL,
			status     TEXT NOT NULL DEFAULT 'pending',
			priority   INTEGER NOT NULL DEFAULT 1,
			item_count INTEGER NOT NULL DEFAULT 0,
			error      TEXT,
			config     TEXT,
			stats      TEXT,
			created_at TEXT NOT NULL,
			started_at TEXT,
			ended_at   TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
		CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);

		CREATE TABLE IF NOT EXISTS job_items (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id  TEXT NOT NULL REFERENCES jobs(id),
			data    TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_items_job ON job_items(job_id);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}

	return &JobManager{
		db:     db,
		logger: logger.With("component", "job_manager"),
	}, nil
}

// CreateJob creates a new job record.
func (jm *JobManager) CreateJob(cfg JobConfig) (*Job, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	job := &Job{
		ID:        uuid.New().String(),
		Type:      cfg.Type,
		URL:       cfg.URL,
		Status:    StatusPending,
		Priority:  cfg.Priority,
		Config:    cfg.Config,
		CreatedAt: time.Now(),
	}

	configJSON, _ := json.Marshal(job.Config)

	_, err := jm.db.Exec(
		`INSERT INTO jobs (id, type, url, status, priority, config, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Type, job.URL, job.Status, job.Priority,
		string(configJSON), job.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	jm.logger.Info("job created", "id", job.ID, "type", job.Type, "url", job.URL)
	return job, nil
}

// GetJob retrieves a job by ID.
func (jm *JobManager) GetJob(id string) (*Job, error) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	row := jm.db.QueryRow(
		`SELECT id, type, url, status, priority, item_count, error, config, stats,
		        created_at, started_at, ended_at
		 FROM jobs WHERE id = ?`, id)

	return jm.scanJob(row)
}

// ListJobs returns jobs optionally filtered by status.
func (jm *JobManager) ListJobs(status string, limit int) ([]*Job, error) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	query := `SELECT id, type, url, status, priority, item_count, error, config, stats,
	                  created_at, started_at, ended_at
	           FROM jobs`
	args := []any{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := jm.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		job, err := jm.scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// UpdateStatus sets a job's status.
func (jm *JobManager) UpdateStatus(id, status string) error {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	var setClause string
	args := []any{status}

	switch status {
	case StatusRunning:
		setClause = "status = ?, started_at = ?"
		args = append(args, time.Now().Format(time.RFC3339))
	case StatusCompleted, StatusFailed, StatusCancelled:
		setClause = "status = ?, ended_at = ?"
		args = append(args, time.Now().Format(time.RFC3339))
	default:
		setClause = "status = ?"
	}

	args = append(args, id)
	// #nosec G202 -- setClause is one of three literals chosen by the switch above;
	// no caller-supplied text reaches the statement, and every value is bound with ?.
	result, err := jm.db.Exec("UPDATE jobs SET "+setClause+" WHERE id = ?", args...)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("job not found: %s", id)
	}
	return nil
}

// CompleteJob marks a job as completed with stats.
func (jm *JobManager) CompleteJob(id string, itemCount int, stats map[string]any) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	statsJSON, _ := json.Marshal(stats)
	_, _ = jm.db.Exec(
		`UPDATE jobs SET status = ?, item_count = ?, stats = ?, ended_at = ? WHERE id = ?`,
		StatusCompleted, itemCount, string(statsJSON), time.Now().Format(time.RFC3339), id)
}

// FailJob marks a job as failed with an error message.
func (jm *JobManager) FailJob(id, errMsg string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	_, _ = jm.db.Exec(
		`UPDATE jobs SET status = ?, error = ?, ended_at = ? WHERE id = ?`,
		StatusFailed, errMsg, time.Now().Format(time.RFC3339), id)
}

// AddJobItem stores a scraped item for a job.
func (jm *JobManager) AddJobItem(jobID string, data map[string]any) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}

	_, err = jm.db.Exec(
		`INSERT INTO job_items (job_id, data, created_at) VALUES (?, ?, ?)`,
		jobID, string(dataJSON), time.Now().Format(time.RFC3339))
	return err
}

// GetJobItems retrieves items for a job with pagination.
func (jm *JobManager) GetJobItems(jobID string, offset, limit int) ([]map[string]any, error) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	rows, err := jm.db.Query(
		`SELECT data FROM job_items WHERE job_id = ? ORDER BY id LIMIT ? OFFSET ?`,
		jobID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []map[string]any
	for rows.Next() {
		var dataJSON string
		if err := rows.Scan(&dataJSON); err != nil {
			return nil, err
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Close closes the database.
func (jm *JobManager) Close() error {
	return jm.db.Close()
}

// --- Row Scanning ---

func (jm *JobManager) scanJob(row *sql.Row) (*Job, error) {
	var (
		job                              Job
		errStr, configJSON, statsJSON    sql.NullString
		createdStr, startedStr, endedStr sql.NullString
	)

	err := row.Scan(&job.ID, &job.Type, &job.URL, &job.Status, &job.Priority,
		&job.ItemCount, &errStr, &configJSON, &statsJSON,
		&createdStr, &startedStr, &endedStr)
	if err != nil {
		return nil, err
	}

	job.Error = errStr.String
	if configJSON.Valid {
		_ = json.Unmarshal([]byte(configJSON.String), &job.Config)
	}
	if statsJSON.Valid {
		_ = json.Unmarshal([]byte(statsJSON.String), &job.Stats)
	}
	if createdStr.Valid {
		t, _ := time.Parse(time.RFC3339, createdStr.String)
		job.CreatedAt = t
	}
	if startedStr.Valid {
		t, _ := time.Parse(time.RFC3339, startedStr.String)
		job.StartedAt = &t
	}
	if endedStr.Valid {
		t, _ := time.Parse(time.RFC3339, endedStr.String)
		job.EndedAt = &t
	}

	return &job, nil
}

func (jm *JobManager) scanJobRow(rows *sql.Rows) (*Job, error) {
	var (
		job                              Job
		errStr, configJSON, statsJSON    sql.NullString
		createdStr, startedStr, endedStr sql.NullString
	)

	err := rows.Scan(&job.ID, &job.Type, &job.URL, &job.Status, &job.Priority,
		&job.ItemCount, &errStr, &configJSON, &statsJSON,
		&createdStr, &startedStr, &endedStr)
	if err != nil {
		return nil, err
	}

	job.Error = errStr.String
	if configJSON.Valid {
		_ = json.Unmarshal([]byte(configJSON.String), &job.Config)
	}
	if statsJSON.Valid {
		_ = json.Unmarshal([]byte(statsJSON.String), &job.Stats)
	}
	if createdStr.Valid {
		t, _ := time.Parse(time.RFC3339, createdStr.String)
		job.CreatedAt = t
	}
	if startedStr.Valid {
		t, _ := time.Parse(time.RFC3339, startedStr.String)
		job.StartedAt = &t
	}
	if endedStr.Valid {
		t, _ := time.Parse(time.RFC3339, endedStr.String)
		job.EndedAt = &t
	}

	return &job, nil
}

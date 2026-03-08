package distributed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Queue is an interface for distributed task queues.
// Implementations can use Redis, in-memory, or any other backend.
type Queue interface {
	// Push adds a task to the queue.
	Push(ctx context.Context, task *Task) error

	// Pop removes and returns the next task. Blocks until available or context cancelled.
	Pop(ctx context.Context) (*Task, error)

	// Len returns the current queue length.
	Len() int

	// Close closes the queue.
	Close() error
}

// InMemoryQueue is an in-memory implementation of the distributed queue.
// Used for single-node operation or testing.
type InMemoryQueue struct {
	tasks  []*Task
	mu     sync.Mutex
	cond   *sync.Cond
	closed bool
	logger *slog.Logger
}

// NewInMemoryQueue creates a new in-memory queue.
func NewInMemoryQueue(logger *slog.Logger) *InMemoryQueue {
	q := &InMemoryQueue{
		logger: logger.With("component", "memory_queue"),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Push adds a task to the queue.
func (q *InMemoryQueue) Push(ctx context.Context, task *Task) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return fmt.Errorf("queue closed")
	}

	if task.ID == "" {
		task.ID = fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	task.Status = "pending"
	task.Created = time.Now()

	// Insert in priority order (lower priority value = higher priority)
	inserted := false
	for i, t := range q.tasks {
		if task.Priority < t.Priority {
			q.tasks = append(q.tasks[:i+1], q.tasks[i:]...)
			q.tasks[i] = task
			inserted = true
			break
		}
	}
	if !inserted {
		q.tasks = append(q.tasks, task)
	}

	q.cond.Signal()
	return nil
}

// Pop removes and returns the next task.
func (q *InMemoryQueue) Pop(ctx context.Context) (*Task, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.tasks) == 0 && !q.closed {
		// Use a channel-based wait with context support
		done := make(chan struct{})
		go func() {
			q.mu.Lock()
			q.cond.Wait()
			q.mu.Unlock()
			close(done)
		}()
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			q.mu.Lock()
			return nil, ctx.Err()
		case <-done:
			q.mu.Lock()
		}
	}

	if q.closed && len(q.tasks) == 0 {
		return nil, fmt.Errorf("queue closed")
	}

	task := q.tasks[0]
	q.tasks = q.tasks[1:]
	return task, nil
}

// Len returns the current queue length.
func (q *InMemoryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.tasks)
}

// Close closes the queue.
func (q *InMemoryQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
	return nil
}

// RedisQueueConfig configures a Redis-backed queue.
type RedisQueueConfig struct {
	Addr     string
	Password string
	DB       int
	Key      string
}

// RedisQueue is a Redis-backed distributed queue.
// This is a placeholder implementation that uses in-memory as fallback.
// To use real Redis, add the go-redis dependency.
type RedisQueue struct {
	inner  *InMemoryQueue
	config *RedisQueueConfig
	logger *slog.Logger
}

// NewRedisQueue creates a Redis-backed queue (falls back to in-memory).
func NewRedisQueue(cfg *RedisQueueConfig, logger *slog.Logger) *RedisQueue {
	logger = logger.With("component", "redis_queue")
	logger.Info("Redis queue initialized (in-memory fallback)",
		"addr", cfg.Addr,
		"key", cfg.Key,
	)

	return &RedisQueue{
		inner:  NewInMemoryQueue(logger),
		config: cfg,
		logger: logger,
	}
}

// Push adds a task to the queue.
func (q *RedisQueue) Push(ctx context.Context, task *Task) error {
	return q.inner.Push(ctx, task)
}

// Pop removes and returns the next task.
func (q *RedisQueue) Pop(ctx context.Context) (*Task, error) {
	return q.inner.Pop(ctx)
}

// Len returns the current queue length.
func (q *RedisQueue) Len() int {
	return q.inner.Len()
}

// Close closes the queue.
func (q *RedisQueue) Close() error {
	return q.inner.Close()
}

// BatchSubmit submits multiple URLs as a single task to the queue.
func BatchSubmit(q Queue, urls []string, batchSize int) error {
	ctx := context.Background()

	for i := 0; i < len(urls); i += batchSize {
		end := i + batchSize
		if end > len(urls) {
			end = len(urls)
		}

		batch := urls[i:end]
		task := &Task{
			Type:     "crawl",
			URLs:     batch,
			Priority: 2,
			Config:   make(map[string]any),
		}

		if err := q.Push(ctx, task); err != nil {
			return fmt.Errorf("submit batch %d: %w", i/batchSize, err)
		}
	}

	return nil
}

// QueueStats returns statistics about the queue.
func QueueStats(q Queue) map[string]any {
	return map[string]any{
		"pending_tasks": q.Len(),
	}
}

// ExportQueue serializes all pending tasks as JSON.
func ExportQueue(q *InMemoryQueue) ([]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return json.Marshal(q.tasks)
}

// ImportQueue loads tasks from JSON into the queue.
func ImportQueue(q *InMemoryQueue, data []byte) error {
	var tasks []*Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return err
	}

	ctx := context.Background()
	for _, task := range tasks {
		if err := q.Push(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

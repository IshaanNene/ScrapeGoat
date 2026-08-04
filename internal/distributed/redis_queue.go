package distributed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Default tuning for RedisQueue.
const (
	// defaultVisibility is how long a claimed task may be in flight before the
	// reaper assumes its worker died and makes it available again.
	defaultVisibility = 5 * time.Minute

	// defaultReapInterval is how often the reaper sweeps for expired claims.
	defaultReapInterval = 30 * time.Second

	// popBlockTimeout bounds a single blocking pop so the loop can observe
	// context cancellation.
	popBlockTimeout = 2 * time.Second
)

// ErrQueueClosed is returned by a closed queue.
var ErrQueueClosed = errors.New("queue closed")

// RedisQueue is a Redis-backed distributed task queue.
//
// This replaces a placeholder that logged "Redis queue initialized" and delegated
// every operation to an in-memory queue — so a multi-process deployment silently
// had no shared queue at all, and nothing survived a restart. A queue named for a
// backend it does not use is worse than no queue.
//
// Delivery is at-least-once. A pop atomically moves the task from the pending list
// to a per-consumer processing list, so a worker that dies mid-task leaves the task
// recoverable rather than losing it. The reaper returns tasks whose visibility
// deadline has passed. Callers must Ack a task they finish, or Nack one they cannot;
// an unacked task comes back.
//
// At-least-once means a task can be delivered twice — once to a worker that stalled
// past its deadline and once to its replacement. Crawl tasks are idempotent (the
// engine's deduplicator absorbs the repeat), which is why at-least-once is the right
// trade here rather than the considerably more expensive exactly-once.
type RedisQueue struct {
	client *redis.Client
	logger *slog.Logger

	pendingKey    string
	processingKey string
	deadlineKey   string

	visibility time.Duration
	reapEvery  time.Duration

	stop   chan struct{}
	closed chan struct{}
}

// RedisQueueConfig configures a Redis-backed queue.
type RedisQueueConfig struct {
	Addr     string
	Password string
	DB       int

	// Key is the namespace prefix for this queue's Redis keys.
	Key string

	// Visibility is how long a claimed task may be in flight before it is
	// considered abandoned. Zero uses the default.
	Visibility time.Duration

	// ReapInterval is how often to sweep for abandoned tasks. Zero uses the
	// default.
	ReapInterval time.Duration
}

// NewRedisQueue connects to Redis and starts the reaper.
//
// It returns an error rather than falling back to an in-memory queue. Silently
// degrading to a non-shared, non-durable queue is how the previous implementation
// turned a configuration mistake into data loss that only showed up in production.
func NewRedisQueue(ctx context.Context, cfg *RedisQueueConfig, logger *slog.Logger) (*RedisQueue, error) {
	if cfg == nil || cfg.Addr == "" {
		return nil, errors.New("redis queue: addr is required")
	}

	key := cfg.Key
	if key == "" {
		key = "scrapegoat:tasks"
	}
	visibility := cfg.Visibility
	if visibility <= 0 {
		visibility = defaultVisibility
	}
	reapEvery := cfg.ReapInterval
	if reapEvery <= 0 {
		reapEvery = defaultReapInterval
	}

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis queue: connect to %s: %w", cfg.Addr, err)
	}

	q := &RedisQueue{
		client:        client,
		logger:        logger.With("component", "redis_queue", "addr", cfg.Addr),
		pendingKey:    key + ":pending",
		processingKey: key + ":processing",
		deadlineKey:   key + ":deadlines",
		visibility:    visibility,
		reapEvery:     reapEvery,
		stop:          make(chan struct{}),
		closed:        make(chan struct{}),
	}

	// reapLoop's lifetime is the queue's, not any caller's request: it stops when
	// Close signals q.stop. A request context would end it at the wrong moment and
	// leave reclaimed-but-unreturned tasks stuck in the processing list.
	go q.reapLoop() //nolint:contextcheck // lifecycle is owned by Close, via q.stop

	q.logger.Info("redis queue connected", "key", key, "visibility", visibility)
	return q, nil
}

// Push adds a task to the queue.
//
// Higher-priority tasks go to the head. Redis lists have no priority ordering, so
// this is a two-bucket approximation rather than the in-memory queue's full sort —
// enough to keep retries behind fresh work without a sorted set on the hot path.
func (q *RedisQueue) Push(ctx context.Context, task *Task) error {
	if q.isClosed() {
		return ErrQueueClosed
	}

	if task.ID == "" {
		task.ID = fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	task.Status = "pending"
	task.Created = time.Now()

	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("redis queue: marshal task: %w", err)
	}

	if task.Priority <= 0 {
		return q.client.RPush(ctx, q.pendingKey, payload).Err()
	}
	return q.client.LPush(ctx, q.pendingKey, payload).Err()
}

// Pop claims the next task, blocking until one is available or ctx is done.
//
// BLMOVE is what makes this recoverable: the task leaves the pending list and
// enters the processing list in one atomic step, so there is no window in which a
// crash loses it.
func (q *RedisQueue) Pop(ctx context.Context) (*Task, error) {
	for {
		if q.isClosed() {
			return nil, ErrQueueClosed
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Bounded block so cancellation and Close are observed promptly.
		payload, err := q.client.BLMove(ctx,
			q.pendingKey, q.processingKey, "LEFT", "RIGHT", popBlockTimeout).Result()

		switch {
		case errors.Is(err, redis.Nil):
			continue // timed out with nothing queued
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, ctx.Err()
		case err != nil:
			return nil, fmt.Errorf("redis queue: pop: %w", err)
		}

		var task Task
		if err := json.Unmarshal([]byte(payload), &task); err != nil {
			// Undecodable payloads must not wedge the queue forever. Drop it from
			// processing and keep going, loudly.
			q.logger.Error("discarding undecodable task", "error", err)
			q.client.LRem(ctx, q.processingKey, 1, payload)
			continue
		}

		// Record the visibility deadline. If this fails, or the worker dies before
		// it lands, the reaper's missing-deadline path still recovers the task.
		deadline := time.Now().Add(q.visibility).UnixNano()
		if err := q.client.HSet(ctx, q.deadlineKey, task.ID, deadline).Err(); err != nil {
			q.logger.Warn("could not record visibility deadline", "task", task.ID, "error", err)
		}

		task.Status = "running"
		return &task, nil
	}
}

// Ack marks a task complete and removes it from the processing list.
func (q *RedisQueue) Ack(ctx context.Context, task *Task) error {
	payload, err := q.payloadFor(task, "pending")
	if err != nil {
		return err
	}

	pipe := q.client.TxPipeline()
	pipe.LRem(ctx, q.processingKey, 1, payload)
	pipe.HDel(ctx, q.deadlineKey, task.ID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis queue: ack %s: %w", task.ID, err)
	}
	return nil
}

// Nack returns a task to the pending queue immediately, rather than waiting out
// its visibility deadline. Used when a worker knows it cannot complete the task.
func (q *RedisQueue) Nack(ctx context.Context, task *Task) error {
	payload, err := q.payloadFor(task, "pending")
	if err != nil {
		return err
	}

	pipe := q.client.TxPipeline()
	pipe.LRem(ctx, q.processingKey, 1, payload)
	pipe.HDel(ctx, q.deadlineKey, task.ID)
	pipe.RPush(ctx, q.pendingKey, payload)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis queue: nack %s: %w", task.ID, err)
	}
	return nil
}

// payloadFor re-marshals a task the way it was stored, so LREM matches the exact
// element. Pop mutates Status, which would otherwise change the encoding and make
// the removal silently match nothing.
func (q *RedisQueue) payloadFor(task *Task, status string) (string, error) {
	stored := *task
	stored.Status = status
	payload, err := json.Marshal(&stored)
	if err != nil {
		return "", fmt.Errorf("redis queue: marshal task %s: %w", task.ID, err)
	}
	return string(payload), nil
}

// Len returns the number of tasks waiting to be claimed. In-flight tasks are not
// counted; use InFlight for those.
func (q *RedisQueue) Len() int {
	n, err := q.client.LLen(context.Background(), q.pendingKey).Result()
	if err != nil {
		q.logger.Warn("queue length unavailable", "error", err)
		return 0
	}
	return int(n)
}

// InFlight returns the number of claimed but unacked tasks.
func (q *RedisQueue) InFlight() int {
	n, err := q.client.LLen(context.Background(), q.processingKey).Result()
	if err != nil {
		return 0
	}
	return int(n)
}

// Close stops the reaper and releases the connection. Tasks left in flight stay in
// Redis and are recovered by whichever process reaps them next — which is the point
// of a shared queue.
func (q *RedisQueue) Close() error {
	select {
	case <-q.closed:
		return nil // already closed
	default:
	}

	close(q.stop)
	<-q.closed
	return q.client.Close()
}

func (q *RedisQueue) isClosed() bool {
	select {
	case <-q.closed:
		return true
	default:
		return false
	}
}

// reapLoop periodically returns abandoned tasks to the pending queue.
func (q *RedisQueue) reapLoop() {
	defer close(q.closed)

	ticker := time.NewTicker(q.reapEvery)
	defer ticker.Stop()

	for {
		select {
		case <-q.stop:
			return
		case <-ticker.C:
			if n := q.reap(context.Background()); n > 0 {
				q.logger.Info("recovered abandoned tasks", "count", n)
			}
		}
	}
}

// reap returns tasks whose visibility deadline has passed, or that have no
// deadline recorded at all — the latter covers a worker that died between the
// atomic claim and the deadline write.
func (q *RedisQueue) reap(ctx context.Context) int {
	payloads, err := q.client.LRange(ctx, q.processingKey, 0, -1).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			q.logger.Warn("reaper could not read processing list", "error", err)
		}
		return 0
	}

	now := time.Now().UnixNano()
	recovered := 0

	for _, payload := range payloads {
		var task Task
		if err := json.Unmarshal([]byte(payload), &task); err != nil {
			q.client.LRem(ctx, q.processingKey, 1, payload)
			continue
		}

		deadline, err := q.client.HGet(ctx, q.deadlineKey, task.ID).Int64()
		switch {
		case errors.Is(err, redis.Nil):
			// No deadline recorded: the claim never completed. Recover it.
		case err != nil:
			continue
		case deadline > now:
			continue // still legitimately in flight
		}

		pipe := q.client.TxPipeline()
		pipe.LRem(ctx, q.processingKey, 1, payload)
		pipe.HDel(ctx, q.deadlineKey, task.ID)
		pipe.RPush(ctx, q.pendingKey, payload)
		if _, err := pipe.Exec(ctx); err != nil {
			q.logger.Warn("could not recover task", "task", task.ID, "error", err)
			continue
		}
		recovered++
	}

	return recovered
}

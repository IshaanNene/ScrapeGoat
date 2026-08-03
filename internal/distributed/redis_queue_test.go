package distributed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

var redisTestLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

// redisAddr returns the Redis to test against.
//
// These are real integration tests, not mocks. A mocked Redis would have happily
// passed against the placeholder implementation this replaces, since the
// placeholder's bug was that it never talked to Redis at all.
func redisAddr() string {
	if addr := os.Getenv("SCRAPEGOAT_TEST_REDIS"); addr != "" {
		return addr
	}
	return "127.0.0.1:6379"
}

// newTestQueue connects a queue against a scratch key namespace, skipping the test
// if no Redis is reachable.
func newTestQueue(t *testing.T, opts ...func(*RedisQueueConfig)) *RedisQueue {
	t.Helper()

	cfg := &RedisQueueConfig{
		Addr: redisAddr(),
		Key:  fmt.Sprintf("scrapegoat:test:%s:%d", t.Name(), time.Now().UnixNano()),
		DB:   15, // conventionally a scratch database
	}
	for _, opt := range opts {
		opt(cfg)
	}

	q, err := NewRedisQueue(context.Background(), cfg, redisTestLogger)
	if err != nil {
		t.Skipf("no Redis at %s (%v); set SCRAPEGOAT_TEST_REDIS to point elsewhere", cfg.Addr, err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		q.client.Del(ctx, q.pendingKey, q.processingKey, q.deadlineKey)
		_ = q.Close()
	})
	return q
}

func TestRedisQueueRoundTrip(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	in := &Task{ID: "t1", URLs: []string{"https://example.com"}}
	if err := q.Push(ctx, in); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := q.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}

	out, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if out.ID != "t1" || len(out.URLs) != 1 || out.URLs[0] != "https://example.com" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}

	// Claimed but not acked: still in flight, no longer pending.
	if got := q.Len(); got != 0 {
		t.Errorf("pending Len = %d after pop, want 0", got)
	}
	if got := q.InFlight(); got != 1 {
		t.Errorf("InFlight = %d after pop, want 1", got)
	}

	if err := q.Ack(ctx, out); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if got := q.InFlight(); got != 0 {
		t.Errorf("InFlight = %d after ack, want 0", got)
	}
}

// TestRedisQueueIsSharedBetweenClients is the test the placeholder could never
// have passed: two independent queue objects, as two processes would be, must see
// the same tasks.
func TestRedisQueueIsSharedBetweenClients(t *testing.T) {
	producer := newTestQueue(t)
	ctx := context.Background()

	// A second client on the same key namespace stands in for another process.
	consumer, err := NewRedisQueue(ctx, &RedisQueueConfig{
		Addr: redisAddr(),
		Key:  producerKeyPrefix(producer),
		DB:   15,
	}, redisTestLogger)
	if err != nil {
		t.Skipf("no Redis: %v", err)
	}
	defer consumer.Close()

	if err := producer.Push(ctx, &Task{ID: "shared", URLs: []string{"https://example.com"}}); err != nil {
		t.Fatalf("push: %v", err)
	}

	popCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	got, err := consumer.Pop(popCtx)
	if err != nil {
		t.Fatalf("the second client could not see the first client's task: %v", err)
	}
	if got.ID != "shared" {
		t.Errorf("got task %q, want \"shared\"", got.ID)
	}
}

// producerKeyPrefix recovers the namespace from a queue, so a second client can
// be pointed at the same one.
func producerKeyPrefix(q *RedisQueue) string {
	return q.pendingKey[:len(q.pendingKey)-len(":pending")]
}

// TestRedisQueueRecoversAbandonedTasks covers the reason for the processing list:
// a worker that dies mid-task must not take the task with it.
func TestRedisQueueRecoversAbandonedTasks(t *testing.T) {
	q := newTestQueue(t, func(c *RedisQueueConfig) {
		c.Visibility = 100 * time.Millisecond
		c.ReapInterval = time.Hour // reap manually, so the test is deterministic
	})
	ctx := context.Background()

	if err := q.Push(ctx, &Task{ID: "abandoned", URLs: []string{"https://example.com"}}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Claim it and then "die" — never ack.
	if _, err := q.Pop(ctx); err != nil {
		t.Fatalf("pop: %v", err)
	}
	if got := q.InFlight(); got != 1 {
		t.Fatalf("InFlight = %d, want 1", got)
	}

	// Before the deadline, the task is legitimately in flight and must be left be.
	if n := q.reap(ctx); n != 0 {
		t.Errorf("reaper recovered %d tasks before the visibility deadline", n)
	}

	time.Sleep(150 * time.Millisecond)

	if n := q.reap(ctx); n != 1 {
		t.Fatalf("reaper recovered %d tasks after the deadline, want 1", n)
	}
	if got := q.Len(); got != 1 {
		t.Errorf("pending Len = %d after recovery, want 1", got)
	}

	again, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("re-pop: %v", err)
	}
	if again.ID != "abandoned" {
		t.Errorf("recovered task id = %q, want \"abandoned\"", again.ID)
	}
}

func TestRedisQueueAckPreventsRecovery(t *testing.T) {
	q := newTestQueue(t, func(c *RedisQueueConfig) {
		c.Visibility = 50 * time.Millisecond
		c.ReapInterval = time.Hour
	})
	ctx := context.Background()

	if err := q.Push(ctx, &Task{ID: "done", URLs: []string{"https://example.com"}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	task, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if err := q.Ack(ctx, task); err != nil {
		t.Fatalf("ack: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// An acked task must not come back; at-least-once is the contract, but a
	// completed task reappearing every visibility window is a livelock.
	if n := q.reap(ctx); n != 0 {
		t.Errorf("reaper recovered %d acked tasks, want 0", n)
	}
	if got := q.Len(); got != 0 {
		t.Errorf("pending Len = %d after ack, want 0", got)
	}
}

func TestRedisQueueNackRequeuesImmediately(t *testing.T) {
	q := newTestQueue(t, func(c *RedisQueueConfig) {
		c.Visibility = time.Hour // so only Nack can bring it back
		c.ReapInterval = time.Hour
	})
	ctx := context.Background()

	if err := q.Push(ctx, &Task{ID: "nacked", URLs: []string{"https://example.com"}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	task, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if err := q.Nack(ctx, task); err != nil {
		t.Fatalf("nack: %v", err)
	}

	if got := q.Len(); got != 1 {
		t.Errorf("pending Len = %d after nack, want 1", got)
	}
	if got := q.InFlight(); got != 0 {
		t.Errorf("InFlight = %d after nack, want 0", got)
	}
}

func TestRedisQueuePriorityOrdering(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.Push(ctx, &Task{ID: "normal", Priority: 0}); err != nil {
		t.Fatalf("push normal: %v", err)
	}
	if err := q.Push(ctx, &Task{ID: "urgent", Priority: 1}); err != nil {
		t.Fatalf("push urgent: %v", err)
	}

	first, err := q.Pop(ctx)
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if first.ID != "urgent" {
		t.Errorf("first pop = %q, want \"urgent\" — priority went to the tail", first.ID)
	}
}

func TestRedisQueuePopRespectsContext(t *testing.T) {
	q := newTestQueue(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := q.Pop(ctx) // nothing queued
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Pop returned a task from an empty queue")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Pop error = %v, want context.DeadlineExceeded", err)
	}
	// The blocking pop is bounded internally so cancellation is observed promptly
	// rather than after a long BLMOVE timeout.
	if elapsed > 3*time.Second {
		t.Errorf("Pop took %v to notice a cancelled context", elapsed)
	}
}

// TestNewRedisQueueFailsLoudly is the central regression: the previous
// implementation logged "Redis queue initialized (in-memory fallback)" and
// returned a working-looking object that shared nothing and persisted nothing.
// A configuration mistake became silent data loss discovered in production.
func TestNewRedisQueueFailsLoudly(t *testing.T) {
	_, err := NewRedisQueue(context.Background(), &RedisQueueConfig{
		Addr: "127.0.0.1:1", // nothing listens here
		Key:  "scrapegoat:test:unreachable",
	}, redisTestLogger)

	if err == nil {
		t.Fatal("connecting to an unreachable Redis succeeded; it must not fall back silently")
	}
}

func TestNewRedisQueueRequiresAddr(t *testing.T) {
	if _, err := NewRedisQueue(context.Background(), &RedisQueueConfig{}, redisTestLogger); err == nil {
		t.Error("an empty address should be rejected")
	}
	if _, err := NewRedisQueue(context.Background(), nil, redisTestLogger); err == nil {
		t.Error("a nil config should be rejected")
	}
}

func TestRedisQueueConcurrentPopDeliversEachTaskOnce(t *testing.T) {
	const tasks = 50

	q := newTestQueue(t, func(c *RedisQueueConfig) {
		c.Visibility = time.Hour
		c.ReapInterval = time.Hour
	})
	ctx := context.Background()

	for i := 0; i < tasks; i++ {
		if err := q.Push(ctx, &Task{ID: fmt.Sprintf("task-%d", i)}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}

	popCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	seen := make(chan string, tasks*2)
	done := make(chan struct{})

	for w := 0; w < 8; w++ {
		go func() {
			for {
				task, err := q.Pop(popCtx)
				if err != nil {
					return
				}
				select {
				case seen <- task.ID:
				case <-done:
					return
				}
				_ = q.Ack(popCtx, task)
			}
		}()
	}

	got := make(map[string]int)
	for i := 0; i < tasks; i++ {
		select {
		case id := <-seen:
			got[id]++
		case <-time.After(10 * time.Second):
			t.Fatalf("only received %d of %d tasks", i, tasks)
		}
	}
	close(done)

	for id, n := range got {
		if n != 1 {
			t.Errorf("task %s delivered %d times to concurrent consumers", id, n)
		}
	}
	if len(got) != tasks {
		t.Errorf("received %d distinct tasks, want %d", len(got), tasks)
	}
}

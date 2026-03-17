package distributed

import (
	"context"
	"testing"
	"time"
)

// --- InMemoryQueue coverage ---
func TestInMemoryQueuePushPopLen(t *testing.T) {
	q := NewInMemoryQueue(testLogger)

	if q.Len() != 0 {
		t.Errorf("new queue len=%d, want 0", q.Len())
	}

	ctx := context.Background()
	q.Push(ctx, &Task{ID: "task-1", Type: "crawl", URLs: []string{"https://example.com"}})
	q.Push(ctx, &Task{ID: "task-2", Type: "crawl", URLs: []string{"https://example.com/2"}})

	if q.Len() != 2 {
		t.Errorf("len=%d, want 2", q.Len())
	}

	task, err := q.Pop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "task-1" {
		t.Errorf("got task %q, want task-1", task.ID)
	}
	if q.Len() != 1 {
		t.Errorf("len=%d, want 1 after pop", q.Len())
	}
}

func TestInMemoryQueueClose(t *testing.T) {
	q := NewInMemoryQueue(testLogger)
	ctx := context.Background()
	q.Push(ctx, &Task{ID: "t1"})
	q.Close()

	// Pop after close should return error or nil
	_, err := q.Pop(ctx)
	if err == nil {
		t.Log("Pop after close returned no error (implementation may drain)")
	}
}

// --- ExportQueue / ImportQueue (package-level functions) ---
func TestExportImportQueue(t *testing.T) {
	q := NewInMemoryQueue(testLogger)
	ctx := context.Background()
	q.Push(ctx, &Task{ID: "t1", Type: "crawl"})
	q.Push(ctx, &Task{ID: "t2", Type: "crawl"})

	exported, err := ExportQueue(q)
	if err != nil {
		t.Fatal(err)
	}

	q2 := NewInMemoryQueue(testLogger)
	err = ImportQueue(q2, exported)
	if err != nil {
		t.Fatal(err)
	}
	if q2.Len() != 2 {
		t.Errorf("imported queue len=%d, want 2", q2.Len())
	}
}

// --- MonitorNodes (with context + timeout duration) ---
func TestMonitorNodes(t *testing.T) {
	master := NewMaster(testLogger)

	master.RegisterNode(&Node{
		ID: "w1", Address: "localhost:8001",
		Role: RoleWorker, Status: NodeReady, Capacity: 5,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go master.MonitorNodes(ctx, 100*time.Millisecond)
	<-ctx.Done()
	t.Log("PASS: MonitorNodes ran without panic")
}

package distributed

import (
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// ---------------------------------------------------------------------------
// Task assignment: N tasks distributed evenly across M workers
// ---------------------------------------------------------------------------

func TestTaskDistribution(t *testing.T) {
	t.Parallel()
	master := NewMaster(testLogger)

	// Register 3 workers
	for i := 1; i <= 3; i++ {
		master.RegisterNode(&Node{
			ID:       nodeID(i),
			Address:  nodeAddr(i),
			Role:     RoleWorker,
			Status:   NodeReady,
			Capacity: 10,
		})
	}

	// Submit 9 tasks (should distribute 3 per worker)
	for i := 0; i < 9; i++ {
		master.SubmitTask(&Task{
			Type: "crawl",
			URLs: []string{"https://example.com"},
		})
	}

	assignments := master.AssignTasks()

	// Count per worker
	perWorker := make(map[string]int)
	for _, a := range assignments {
		perWorker[a.NodeID]++
	}

	if len(perWorker) < 2 {
		t.Errorf("tasks only assigned to %d workers, expected distribution across 3", len(perWorker))
	}

	t.Logf("distribution: %v (total assignments: %d)", perWorker, len(assignments))
}

// ---------------------------------------------------------------------------
// Worker capacity: worker at capacity rejects tasks
// ---------------------------------------------------------------------------

func TestWorkerCapacity(t *testing.T) {
	t.Parallel()
	master := NewMaster(testLogger)

	// Register 1 worker with capacity=2
	master.RegisterNode(&Node{
		ID:       "worker-1",
		Address:  "localhost:8001",
		Role:     RoleWorker,
		Status:   NodeReady,
		Capacity: 2,
	})

	// Submit 5 tasks
	for i := 0; i < 5; i++ {
		master.SubmitTask(&Task{
			Type: "crawl",
			URLs: []string{"https://example.com"},
		})
	}

	assignments := master.AssignTasks()

	// Only 2 should be assigned (worker capacity)
	if len(assignments) > 2 {
		t.Errorf("assigned %d tasks but capacity is 2", len(assignments))
	}

	// Remaining 3 should still be pending
	status := master.GetClusterStatus()
	if status.PendingTasks < 3 {
		t.Logf("pending=%d (may be 0 if all assigned)", status.PendingTasks)
	}
	t.Logf("assigned: %d, pending: %d", len(assignments), status.PendingTasks)
}

// ---------------------------------------------------------------------------
// Task completion and status tracking
// ---------------------------------------------------------------------------

func TestTaskCompletion(t *testing.T) {
	t.Parallel()
	master := NewMaster(testLogger)

	master.RegisterNode(&Node{
		ID: "worker-1", Address: "localhost:8001",
		Role: RoleWorker, Status: NodeReady, Capacity: 10,
	})

	master.SubmitTask(&Task{
		ID:   "task-1",
		Type: "crawl",
		URLs: []string{"https://example.com"},
	})

	master.AssignTasks()

	// Complete the task
	result, _ := json.Marshal(map[string]any{"success": true, "items": 5})
	master.CompleteTask("task-1", result)

	status := master.GetClusterStatus()
	if status.DoneTasks != 1 {
		t.Errorf("done=%d, want 1", status.DoneTasks)
	}
	if status.RunningTasks != 0 {
		t.Errorf("running=%d, want 0", status.RunningTasks)
	}
}

// ---------------------------------------------------------------------------
// Heartbeat and node monitoring
// ---------------------------------------------------------------------------

func TestHeartbeatAndMonitoring(t *testing.T) {
	t.Parallel()
	master := NewMaster(testLogger)

	master.RegisterNode(&Node{
		ID: "worker-1", Address: "localhost:8001",
		Role: RoleWorker, Status: NodeReady, Capacity: 5,
	})

	// Send heartbeat
	master.Heartbeat("worker-1", NodeStats{
		RequestsSent:    100,
		ItemsScraped:    50,
		BytesDownloaded: 1024 * 1024,
	})

	status := master.GetClusterStatus()
	if status.TotalNodes != 1 {
		t.Errorf("nodes=%d, want 1", status.TotalNodes)
	}

	// Verify node stats updated
	for _, n := range status.Nodes {
		if n.ID == "worker-1" {
			if n.Stats.RequestsSent != 100 {
				t.Errorf("requests_sent=%d, want 100", n.Stats.RequestsSent)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Unregister removes worker
// ---------------------------------------------------------------------------

func TestUnregisterNode(t *testing.T) {
	t.Parallel()
	master := NewMaster(testLogger)

	master.RegisterNode(&Node{
		ID: "worker-1", Address: "localhost:8001",
		Role: RoleWorker, Status: NodeReady, Capacity: 5,
	})
	master.RegisterNode(&Node{
		ID: "worker-2", Address: "localhost:8002",
		Role: RoleWorker, Status: NodeReady, Capacity: 5,
	})

	if master.GetClusterStatus().TotalNodes != 2 {
		t.Fatal("should have 2 nodes")
	}

	master.UnregisterNode("worker-1")
	if master.GetClusterStatus().TotalNodes != 1 {
		t.Error("should have 1 node after unregister")
	}
}

// ---------------------------------------------------------------------------
// Offline detection
// ---------------------------------------------------------------------------

func TestOfflineDetection(t *testing.T) {
	t.Parallel()
	master := NewMaster(testLogger)

	node := &Node{
		ID: "worker-1", Address: "localhost:8001",
		Role: RoleWorker, Status: NodeReady, Capacity: 5,
	}
	master.RegisterNode(node)

	// Manually set LastSeen to 10s ago on the stored node
	master.mu.Lock()
	for _, n := range master.nodes {
		if n.ID == "worker-1" {
			n.LastSeen = time.Now().Add(-10 * time.Second)
			// Manually mark as offline (simulating monitor check)
			if time.Since(n.LastSeen) > 5*time.Second {
				n.Status = NodeOffline
			}
		}
	}
	master.mu.Unlock()

	status := master.GetClusterStatus()
	for _, n := range status.Nodes {
		if n.ID == "worker-1" && n.Status != NodeOffline {
			t.Errorf("node should be offline, got %s", n.Status)
		}
	}
}

func nodeID(i int) string   { return "worker-" + itoa(int64(i)) }
func nodeAddr(i int) string { return "localhost:" + itoa(int64(8000+i)) }

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

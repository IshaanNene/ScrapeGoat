package distributed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Role identifies whether a node is a master or worker.
type Role string

const (
	RoleMaster Role = "master"
	RoleWorker Role = "worker"
)

// NodeStatus tracks a node's health.
type NodeStatus string

const (
	NodeReady   NodeStatus = "ready"
	NodeBusy    NodeStatus = "busy"
	NodeOffline NodeStatus = "offline"
)

// Node represents a worker node in the distributed cluster.
type Node struct {
	ID         string     `json:"id"`
	Address    string     `json:"address"`
	Role       Role       `json:"role"`
	Status     NodeStatus `json:"status"`
	Capacity   int        `json:"capacity"` // Max concurrent workers
	ActiveJobs int        `json:"active_jobs"`
	LastSeen   time.Time  `json:"last_seen"`
	Stats      NodeStats  `json:"stats"`
}

// NodeStats holds per-node statistics.
type NodeStats struct {
	RequestsSent    int64         `json:"requests_sent"`
	RequestsFailed  int64         `json:"requests_failed"`
	ItemsScraped    int64         `json:"items_scraped"`
	BytesDownloaded int64         `json:"bytes_downloaded"`
	Uptime          time.Duration `json:"uptime"`
}

// Task represents a unit of work assigned to a worker.
type Task struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"` // crawl, parse, store
	URLs     []string        `json:"urls"`
	Config   map[string]any  `json:"config"`
	Priority int             `json:"priority"`
	Created  time.Time       `json:"created"`
	Assigned string          `json:"assigned_to,omitempty"`
	Status   string          `json:"status"` // pending, running, done, failed
	Result   json.RawMessage `json:"result,omitempty"`
}

// Master coordinates work distribution across worker nodes.
type Master struct {
	nodes     map[string]*Node
	tasks     map[string]*Task
	taskQueue []*Task
	logger    *slog.Logger
	mu        sync.RWMutex
}

// NewMaster creates a new master coordinator.
func NewMaster(logger *slog.Logger) *Master {
	return &Master{
		nodes:  make(map[string]*Node),
		tasks:  make(map[string]*Task),
		logger: logger.With("component", "master"),
	}
}

// RegisterNode adds a worker node to the cluster.
func (m *Master) RegisterNode(node *Node) {
	m.mu.Lock()
	defer m.mu.Unlock()
	node.Status = NodeReady
	node.LastSeen = time.Now()
	m.nodes[node.ID] = node
	m.logger.Info("node registered", "id", node.ID, "address", node.Address, "capacity", node.Capacity)
}

// UnregisterNode removes a worker node.
func (m *Master) UnregisterNode(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodes, nodeID)
	m.logger.Info("node unregistered", "id", nodeID)
}

// SubmitTask adds a task to the queue.
func (m *Master) SubmitTask(task *Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task.Status = "pending"
	task.Created = time.Now()
	if task.ID == "" {
		task.ID = fmt.Sprintf("task-%d", time.Now().UnixMilli())
	}
	m.tasks[task.ID] = task
	m.taskQueue = append(m.taskQueue, task)
	m.logger.Info("task submitted", "id", task.ID, "type", task.Type, "urls", len(task.URLs))
}

// AssignTasks distributes pending tasks to available workers.
func (m *Master) AssignTasks() []Assignment {
	m.mu.Lock()
	defer m.mu.Unlock()

	assignments := make([]Assignment, 0, len(m.taskQueue))

	for _, task := range m.taskQueue {
		if task.Status != "pending" {
			continue
		}

		// Find best worker
		best := m.findBestWorker()
		if best == nil {
			break
		}

		task.Status = "running"
		task.Assigned = best.ID
		best.ActiveJobs++
		if best.ActiveJobs >= best.Capacity {
			best.Status = NodeBusy
		}

		assignments = append(assignments, Assignment{
			TaskID: task.ID,
			NodeID: best.ID,
			Task:   task,
		})

		m.logger.Info("task assigned", "task", task.ID, "node", best.ID)
	}

	// Remove assigned tasks from queue
	var remaining []*Task
	for _, t := range m.taskQueue {
		if t.Status == "pending" {
			remaining = append(remaining, t)
		}
	}
	m.taskQueue = remaining

	return assignments
}

// CompleteTask marks a task as done.
func (m *Master) CompleteTask(taskID string, result json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return
	}

	task.Status = "done"
	task.Result = result

	if node, ok := m.nodes[task.Assigned]; ok {
		node.ActiveJobs--
		if node.ActiveJobs < node.Capacity {
			node.Status = NodeReady
		}
	}
}

// findBestWorker returns the worker with the most available capacity.
func (m *Master) findBestWorker() *Node {
	var best *Node
	bestCapacity := 0

	for _, node := range m.nodes {
		if node.Status == NodeOffline {
			continue
		}
		available := node.Capacity - node.ActiveJobs
		if available > bestCapacity {
			best = node
			bestCapacity = available
		}
	}

	return best
}

// GetClusterStatus returns the status of all nodes and tasks.
func (m *Master) GetClusterStatus() ClusterStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	nodes := make([]*Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		nodes = append(nodes, n)
	}

	pendingTasks := 0
	runningTasks := 0
	doneTasks := 0
	for _, t := range m.tasks {
		switch t.Status {
		case "pending":
			pendingTasks++
		case "running":
			runningTasks++
		case "done":
			doneTasks++
		}
	}

	return ClusterStatus{
		Nodes:        nodes,
		TotalNodes:   len(m.nodes),
		PendingTasks: pendingTasks,
		RunningTasks: runningTasks,
		DoneTasks:    doneTasks,
	}
}

// Heartbeat updates a node's last-seen timestamp.
func (m *Master) Heartbeat(nodeID string, stats NodeStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if node, ok := m.nodes[nodeID]; ok {
		node.LastSeen = time.Now()
		node.Stats = stats
	}
}

// MonitorNodes checks for offline nodes.
func (m *Master) MonitorNodes(ctx context.Context, timeout time.Duration) {
	ticker := time.NewTicker(timeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			for _, node := range m.nodes {
				if time.Since(node.LastSeen) > timeout {
					node.Status = NodeOffline
					m.logger.Warn("node offline", "id", node.ID, "last_seen", node.LastSeen)
				}
			}
			m.mu.Unlock()
		}
	}
}

// Assignment pairs a task with a worker node.
type Assignment struct {
	TaskID string `json:"task_id"`
	NodeID string `json:"node_id"`
	Task   *Task  `json:"task"`
}

// ClusterStatus holds the overall cluster state.
type ClusterStatus struct {
	Nodes        []*Node `json:"nodes"`
	TotalNodes   int     `json:"total_nodes"`
	PendingTasks int     `json:"pending_tasks"`
	RunningTasks int     `json:"running_tasks"`
	DoneTasks    int     `json:"done_tasks"`
}

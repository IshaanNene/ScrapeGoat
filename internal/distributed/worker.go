package distributed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Worker represents a distributed crawl worker that pulls tasks from a master
// and executes crawl jobs independently.
//
// Architecture:
//
//	Master Node
//	     ↓
//	Redis Queue
//	↓      ↓      ↓
//	Worker Worker Worker
type Worker struct {
	id         string
	masterAddr string
	capacity   int
	status     NodeStatus
	logger     *slog.Logger
	client     *http.Client
	stats      NodeStats
	startTime  time.Time
	cancel     context.CancelFunc
	mu         sync.RWMutex
	taskCh     chan *Task
	doneCh     chan string
	crawlFunc  func(task *Task) error
}

// WorkerConfig configures a distributed worker.
type WorkerConfig struct {
	ID         string
	MasterAddr string
	Capacity   int
	Heartbeat  time.Duration
}

// DefaultWorkerConfig returns sensible defaults.
func DefaultWorkerConfig() *WorkerConfig {
	return &WorkerConfig{
		ID:         fmt.Sprintf("worker-%d", time.Now().UnixMilli()),
		MasterAddr: "http://localhost:8081",
		Capacity:   10,
		Heartbeat:  5 * time.Second,
	}
}

// NewWorker creates a new distributed worker.
func NewWorker(cfg *WorkerConfig, logger *slog.Logger) *Worker {
	if cfg == nil {
		cfg = DefaultWorkerConfig()
	}

	return &Worker{
		id:         cfg.ID,
		masterAddr: cfg.MasterAddr,
		capacity:   cfg.Capacity,
		status:     NodeReady,
		logger:     logger.With("component", "worker", "worker_id", cfg.ID),
		client:     &http.Client{Timeout: 10 * time.Second},
		startTime:  time.Now(),
		taskCh:     make(chan *Task, cfg.Capacity),
		doneCh:     make(chan string, cfg.Capacity),
	}
}

// SetCrawlFunc sets the function that executes crawl tasks.
func (w *Worker) SetCrawlFunc(fn func(task *Task) error) {
	w.crawlFunc = fn
}

// Start starts the worker: registers with master, begins heartbeat and task polling.
func (w *Worker) Start(ctx context.Context) error {
	ctx, w.cancel = context.WithCancel(ctx)

	w.logger.Info("worker starting",
		"master", w.masterAddr,
		"capacity", w.capacity,
	)

	// Register with master
	if err := w.register(); err != nil {
		return fmt.Errorf("register with master: %w", err)
	}

	// Start heartbeat
	go w.heartbeatLoop(ctx)

	// Start task executor
	go w.taskExecutor(ctx)

	// Start task poller
	go w.taskPoller(ctx)

	w.logger.Info("worker started")
	return nil
}

// Stop gracefully stops the worker.
func (w *Worker) Stop() {
	w.logger.Info("worker stopping")
	if w.cancel != nil {
		w.cancel()
	}
	w.unregister()
	w.logger.Info("worker stopped")
}

// register sends registration to the master.
func (w *Worker) register() error {
	node := &Node{
		ID:       w.id,
		Address:  w.id, // In a real setup, this would be the worker's host:port
		Role:     RoleWorker,
		Status:   NodeReady,
		Capacity: w.capacity,
		LastSeen: time.Now(),
	}

	data, _ := json.Marshal(node)
	resp, err := w.client.Post(w.masterAddr+"/api/register", "application/json",
		jsonReader(data))
	if err != nil {
		w.logger.Warn("registration failed (master may not be running)", "error", err)
		return nil // Non-fatal: master might start later
	}
	defer resp.Body.Close()

	w.logger.Info("registered with master", "status", resp.StatusCode)
	return nil
}

// unregister removes the worker from the master.
func (w *Worker) unregister() {
	resp, err := w.client.Post(w.masterAddr+"/api/unregister/"+w.id, "application/json", nil)
	if err != nil {
		w.logger.Debug("unregister failed", "error", err)
		return
	}
	resp.Body.Close()
}

// heartbeatLoop sends periodic heartbeats to the master.
func (w *Worker) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.RLock()
			stats := w.stats
			stats.Uptime = time.Since(w.startTime)
			w.mu.RUnlock()

			data, _ := json.Marshal(map[string]any{
				"node_id": w.id,
				"stats":   stats,
			})

			resp, err := w.client.Post(w.masterAddr+"/api/heartbeat", "application/json",
				jsonReader(data))
			if err != nil {
				w.logger.Debug("heartbeat failed", "error", err)
				continue
			}
			resp.Body.Close()
		}
	}
}

// taskPoller periodically fetches tasks from the master.
func (w *Worker) taskPoller(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollTasks()
		}
	}
}

// pollTasks fetches available tasks from the master.
func (w *Worker) pollTasks() {
	resp, err := w.client.Get(w.masterAddr + "/api/tasks/" + w.id)
	if err != nil {
		return // Silent — master may not have tasks
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var tasks []*Task
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return
	}

	for _, task := range tasks {
		select {
		case w.taskCh <- task:
			w.logger.Info("task received", "task_id", task.ID, "urls", len(task.URLs))
		default:
			w.logger.Warn("task queue full, skipping", "task_id", task.ID)
		}
	}
}

// taskExecutor processes tasks from the task channel.
func (w *Worker) taskExecutor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-w.taskCh:
			w.executeTask(task)
		}
	}
}

// executeTask runs a single crawl task.
func (w *Worker) executeTask(task *Task) {
	w.logger.Info("executing task", "task_id", task.ID, "type", task.Type, "urls", len(task.URLs))

	w.mu.Lock()
	w.status = NodeBusy
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.status = NodeReady
		w.mu.Unlock()
	}()

	var err error
	if w.crawlFunc != nil {
		err = w.crawlFunc(task)
	}

	// Report completion
	result := map[string]any{
		"success": err == nil,
	}
	if err != nil {
		result["error"] = err.Error()
		w.mu.Lock()
		w.stats.RequestsFailed++
		w.mu.Unlock()
	} else {
		w.mu.Lock()
		w.stats.ItemsScraped += int64(len(task.URLs))
		w.mu.Unlock()
	}

	resultJSON, _ := json.Marshal(result)
	data, _ := json.Marshal(map[string]any{
		"task_id": task.ID,
		"result":  json.RawMessage(resultJSON),
	})

	resp, postErr := w.client.Post(w.masterAddr+"/api/complete", "application/json",
		jsonReader(data))
	if postErr != nil {
		w.logger.Warn("task completion report failed", "error", postErr)
		return
	}
	resp.Body.Close()

	w.logger.Info("task completed", "task_id", task.ID, "success", err == nil)
}

// Stats returns the worker's current statistics.
func (w *Worker) Stats() NodeStats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	stats := w.stats
	stats.Uptime = time.Since(w.startTime)
	return stats
}

// ID returns the worker's identifier.
func (w *Worker) ID() string {
	return w.id
}

// --- Master HTTP API ---

// MasterAPI exposes the master coordinator as an HTTP API for workers.
type MasterAPI struct {
	master *Master
	logger *slog.Logger
}

// NewMasterAPI creates a new master HTTP API wrapper.
func NewMasterAPI(master *Master, logger *slog.Logger) *MasterAPI {
	return &MasterAPI{
		master: master,
		logger: logger.With("component", "master_api"),
	}
}

// ServeMux returns an HTTP handler for the master API.
func (api *MasterAPI) ServeMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/register", api.handleRegister)
	mux.HandleFunc("/api/unregister/", api.handleUnregister)
	mux.HandleFunc("/api/heartbeat", api.handleHeartbeat)
	mux.HandleFunc("/api/tasks/", api.handleGetTasks)
	mux.HandleFunc("/api/complete", api.handleComplete)
	mux.HandleFunc("/api/submit", api.handleSubmit)
	mux.HandleFunc("/api/status", api.handleStatus)
	mux.HandleFunc("/api/scale", api.handleScale)

	return mux
}

// Start starts the master API server on the given address.
func (api *MasterAPI) Start(addr string) error {
	api.logger.Info("master API starting", "addr", addr)
	go func() {
		if err := http.ListenAndServe(addr, api.ServeMux()); err != nil {
			api.logger.Error("master API error", "error", err)
		}
	}()
	return nil
}

func (api *MasterAPI) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var node Node
	if err := json.NewDecoder(r.Body).Decode(&node); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	api.master.RegisterNode(&node)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
}

func (api *MasterAPI) handleUnregister(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Path[len("/api/unregister/"):]
	api.master.UnregisterNode(nodeID)
	w.WriteHeader(http.StatusOK)
}

func (api *MasterAPI) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		NodeID string    `json:"node_id"`
		Stats  NodeStats `json:"stats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	api.master.Heartbeat(payload.NodeID, payload.Stats)
	w.WriteHeader(http.StatusOK)
}

func (api *MasterAPI) handleGetTasks(w http.ResponseWriter, r *http.Request) {
	// Assign pending tasks
	assignments := api.master.AssignTasks()

	workerID := r.URL.Path[len("/api/tasks/"):]
	var workerTasks []*Task
	for _, a := range assignments {
		if a.NodeID == workerID {
			workerTasks = append(workerTasks, a.Task)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workerTasks)
}

func (api *MasterAPI) handleComplete(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		TaskID string          `json:"task_id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	api.master.CompleteTask(payload.TaskID, payload.Result)
	w.WriteHeader(http.StatusOK)
}

func (api *MasterAPI) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var task Task
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	api.master.SubmitTask(&task)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"task_id": task.ID})
}

func (api *MasterAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := api.master.GetClusterStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (api *MasterAPI) handleScale(w http.ResponseWriter, r *http.Request) {
	status := api.master.GetClusterStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"current_workers": status.TotalNodes,
		"status":          "scale command received",
	})
}

// jsonReader wraps JSON bytes in an io.Reader.
func jsonReader(data []byte) *jsonReaderImpl {
	return &jsonReaderImpl{data: data}
}

type jsonReaderImpl struct {
	data []byte
	pos  int
}

func (r *jsonReaderImpl) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, fmt.Errorf("EOF")
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, fmt.Errorf("EOF")
	}
	return n, nil
}

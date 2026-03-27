package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Server provides a REST API for external control of the crawler.
type Server struct {
	mux    *http.ServeMux
	port   int
	logger *slog.Logger

	// Engine interface (set at runtime)
	engineCtrl EngineController

	// Job tracking
	jobs   map[string]*Job
	jobsMu sync.RWMutex
}

// EngineController is the interface the API uses to control the engine.
type EngineController interface {
	Start() error
	Stop()
	Pause()
	Resume()
	AddSeed(url string) error
	GetState() string
	GetStats() map[string]any
}

// Job tracks a crawl job.
type Job struct {
	ID        string         `json:"id"`
	Status    string         `json:"status"`
	Seeds     []string       `json:"seeds"`
	Config    map[string]any `json:"config,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	Stats     map[string]any `json:"stats,omitempty"`
}

// NewServer creates a new API server.
func NewServer(port int, logger *slog.Logger) *Server {
	s := &Server{
		mux:    http.NewServeMux(),
		port:   port,
		logger: logger.With("component", "api_server"),
		jobs:   make(map[string]*Job),
	}

	s.registerRoutes()
	return s
}

// SetEngine sets the engine controller.
func (s *Server) SetEngine(engine EngineController) {
	s.engineCtrl = engine
}

// Start starts the API server.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	s.logger.Info("API server starting", "addr", addr)

	go func() {
		server := &http.Server{
			Addr:              addr,
			Handler:           s.mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
		}
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("API server error", "error", err)
		}
	}()

	return nil
}

func (s *Server) registerRoutes() {
	// Health
	s.mux.HandleFunc("GET /api/health", s.handleHealth)

	// Engine control
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("POST /api/start", s.handleStart)
	s.mux.HandleFunc("POST /api/stop", s.handleStop)
	s.mux.HandleFunc("POST /api/pause", s.handlePause)
	s.mux.HandleFunc("POST /api/resume", s.handleResume)

	// Jobs
	s.mux.HandleFunc("POST /api/jobs", s.handleCreateJob)
	s.mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	s.mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)

	// Seeds
	s.mux.HandleFunc("POST /api/seeds", s.handleAddSeed)

	// Stats
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": "dev",
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.engineCtrl == nil {
		s.jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not initialized"})
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"state": s.engineCtrl.GetState(),
		"stats": s.engineCtrl.GetStats(),
	})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if s.engineCtrl == nil {
		s.jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not initialized"})
		return
	}
	if err := s.engineCtrl.Start(); err != nil {
		s.jsonResponse(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.engineCtrl != nil {
		s.engineCtrl.Stop()
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if s.engineCtrl != nil {
		s.engineCtrl.Pause()
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if s.engineCtrl != nil {
		s.engineCtrl.Resume()
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (s *Server) handleAddSeed(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if s.engineCtrl == nil {
		s.jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not initialized"})
		return
	}
	if err := s.engineCtrl.AddSeed(body.URL); err != nil {
		s.jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "added", "url": body.URL})
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seeds  []string       `json:"seeds"`
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	job := &Job{
		ID:        fmt.Sprintf("job-%d", time.Now().UnixMilli()),
		Status:    "pending",
		Seeds:     body.Seeds,
		Config:    body.Config,
		StartedAt: time.Now(),
	}

	s.jobsMu.Lock()
	s.jobs[job.ID] = job
	s.jobsMu.Unlock()

	s.jsonResponse(w, http.StatusCreated, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()

	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.jsonResponse(w, http.StatusOK, jobs)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.jobsMu.RLock()
	job, ok := s.jobs[id]
	s.jobsMu.RUnlock()

	if !ok {
		s.jsonResponse(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	s.jsonResponse(w, http.StatusOK, job)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.engineCtrl == nil {
		s.jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not initialized"})
		return
	}
	s.jsonResponse(w, http.StatusOK, s.engineCtrl.GetStats())
}

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

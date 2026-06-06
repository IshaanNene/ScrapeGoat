// Package apiserver implements a REST and WebSocket API server for ScrapeGoat,
// enabling microservice deployment and programmatic access to crawling,
// extraction, and job management.
package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
)

// Server is the REST/WebSocket API server.
type Server struct {
	cfg        *config.Config
	logger     *slog.Logger
	jobManager *JobManager
	apiKey     string
	limiters   map[string]*rate.Limiter
	limiterMu  sync.RWMutex
	rateRPS    int
	upgrader   websocket.Upgrader
	wsClients  map[string]map[*websocket.Conn]bool // jobID -> connections
	wsMu       sync.RWMutex
}

// NewServer creates a new API server.
func NewServer(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	dbPath := cfg.APIServer.DBPath
	if dbPath == "" {
		dbPath = "./scrapegoat_jobs.db"
	}

	jobMgr, err := NewJobManager(dbPath, logger)
	if err != nil {
		return nil, fmt.Errorf("create job manager: %w", err)
	}

	rps := cfg.APIServer.RateRPS
	if rps <= 0 {
		rps = 10
	}

	return &Server{
		cfg:        cfg,
		logger:     logger.With("component", "api_server"),
		jobManager: jobMgr,
		apiKey:     cfg.APIServer.APIKey,
		limiters:   make(map[string]*rate.Limiter),
		rateRPS:    rps,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		wsClients: make(map[string]map[*websocket.Conn]bool),
	}, nil
}

// Start starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// API routes.
	mux.HandleFunc("POST /v1/crawl", s.withAuth(s.withRateLimit(s.handleCrawl)))
	mux.HandleFunc("POST /v1/extract", s.withAuth(s.withRateLimit(s.handleExtract)))
	mux.HandleFunc("POST /v1/batch", s.withAuth(s.withRateLimit(s.handleBatch)))
	mux.HandleFunc("GET /v1/jobs/{id}", s.withAuth(s.handleGetJob))
	mux.HandleFunc("DELETE /v1/jobs/{id}", s.withAuth(s.handleCancelJob))
	mux.HandleFunc("GET /v1/jobs/{id}/items", s.withAuth(s.handleGetJobItems))
	mux.HandleFunc("GET /v1/jobs", s.withAuth(s.handleListJobs))
	mux.HandleFunc("POST /v1/screenshot", s.withAuth(s.withRateLimit(s.handleScreenshot)))
	mux.HandleFunc("POST /v1/seo-audit", s.withAuth(s.withRateLimit(s.handleSEOAudit)))
	mux.HandleFunc("POST /v1/sitemap", s.withAuth(s.withRateLimit(s.handleSitemap)))

	// WebSocket.
	mux.HandleFunc("GET /v1/ws/{id}", s.withAuth(s.handleWebSocket))

	// Health.
	mux.HandleFunc("GET /health", s.handleHealth)

	// CORS.
	var handler http.Handler = mux
	if s.cfg.APIServer.CORS {
		handler = s.corsMiddleware(mux)
	}

	addr := fmt.Sprintf(":%d", s.cfg.APIServer.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("API server started", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.jobManager.Close()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// --- Middleware ---

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" {
			next(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}

		if key != s.apiKey {
			s.jsonError(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}

		next(w, r)
	}
}

func (s *Server) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = r.RemoteAddr
		}

		limiter := s.getLimiter(key)
		if !limiter.Allow() {
			s.jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		next(w, r)
	}
}

func (s *Server) getLimiter(key string) *rate.Limiter {
	s.limiterMu.RLock()
	l, ok := s.limiters[key]
	s.limiterMu.RUnlock()
	if ok {
		return l
	}

	s.limiterMu.Lock()
	defer s.limiterMu.Unlock()
	l = rate.NewLimiter(rate.Limit(s.rateRPS), s.rateRPS*2)
	s.limiters[key] = l
	return l
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// --- Response Helpers ---

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) jsonError(w http.ResponseWriter, status int, message string) {
	s.jsonResponse(w, status, map[string]any{
		"error":   http.StatusText(status),
		"message": message,
		"status":  status,
	})
}

// --- Health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": config.Version,
		"time":    time.Now().Format(time.RFC3339),
	})
}

// --- WebSocket ---

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		s.jsonError(w, http.StatusBadRequest, "job ID required")
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("websocket upgrade", "error", err)
		return
	}

	s.wsMu.Lock()
	if s.wsClients[jobID] == nil {
		s.wsClients[jobID] = make(map[*websocket.Conn]bool)
	}
	s.wsClients[jobID][conn] = true
	s.wsMu.Unlock()

	s.logger.Debug("websocket client connected", "job_id", jobID)

	defer func() {
		s.wsMu.Lock()
		delete(s.wsClients[jobID], conn)
		if len(s.wsClients[jobID]) == 0 {
			delete(s.wsClients, jobID)
		}
		s.wsMu.Unlock()
		conn.Close()
	}()

	// Keep alive — read pump.
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// BroadcastJobEvent sends an event to all WebSocket clients watching a job.
func (s *Server) BroadcastJobEvent(jobID string, event map[string]any) {
	s.wsMu.RLock()
	clients := s.wsClients[jobID]
	s.wsMu.RUnlock()

	for conn := range clients {
		if err := conn.WriteJSON(event); err != nil {
			s.logger.Debug("websocket write error", "error", err)
			conn.Close()
			s.wsMu.Lock()
			delete(s.wsClients[jobID], conn)
			s.wsMu.Unlock()
		}
	}
}

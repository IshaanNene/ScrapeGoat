// Package apiserver implements a REST and WebSocket API server for ScrapeGoat,
// enabling microservice deployment and programmatic access to crawling,
// extraction, and job management.
package apiserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
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
	cfg            *config.Config
	logger         *slog.Logger
	jobManager     *JobManager
	apiKey         string
	limiters       map[string]*rate.Limiter
	limiterMu      sync.RWMutex
	rateRPS        int
	allowedOrigins map[string]bool
	wildcardOrigin bool
	upgrader       websocket.Upgrader
	wsClients      map[string]map[*websocket.Conn]bool // jobID -> connections
	wsMu           sync.RWMutex
}

// requestConfig returns a copy of the server's configuration to use for a single
// request or job.
//
// Handlers used to build a fresh config.DefaultConfig() for every fetch, which
// silently discarded every setting the operator supplied — safety policy, request
// timeouts, user agents, proxy configuration — on all API-driven work. Deriving
// from s.cfg means an operator who disallows private addresses actually gets that
// on the REST path too.
//
// The copy is shallow. Handlers only ever replace slice fields wholesale (never
// append in place), so they cannot mutate the server's configuration through it.
func (s *Server) requestConfig() *config.Config {
	cfg := *s.cfg
	return &cfg
}

// ErrNoAPIKey is returned when the server is asked to start without an API key and
// without an explicit opt-out.
var ErrNoAPIKey = errors.New("api server requires an API key")

// NewServer creates a new API server.
//
// It fails closed: without an API key, and without APIServer.AllowNoAuth set, it
// refuses to start. An empty key previously meant "no authentication", which turns
// every endpoint — including the crawl endpoint, which fetches arbitrary URLs — into
// something any web page the operator visits can drive.
func NewServer(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if cfg.APIServer.APIKey == "" && !cfg.APIServer.AllowNoAuth {
		return nil, fmt.Errorf("%w: set api_server.api_key, pass --api-key, or set "+
			"SCRAPEGOAT_API_KEY; to run without authentication anyway, pass "+
			"--insecure-no-auth", ErrNoAPIKey)
	}

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

	allowedOrigins := make(map[string]bool, len(cfg.APIServer.AllowedOrigins))
	wildcardOrigin := false
	for _, o := range cfg.APIServer.AllowedOrigins {
		if o == "*" {
			wildcardOrigin = true
			continue
		}
		allowedOrigins[strings.ToLower(strings.TrimSuffix(o, "/"))] = true
	}

	s := &Server{
		cfg:            cfg,
		logger:         logger.With("component", "api_server"),
		jobManager:     jobMgr,
		apiKey:         cfg.APIServer.APIKey,
		limiters:       make(map[string]*rate.Limiter),
		rateRPS:        rps,
		allowedOrigins: allowedOrigins,
		wildcardOrigin: wildcardOrigin,
		wsClients:      make(map[string]map[*websocket.Conn]bool),
	}

	// A WebSocket upgrade is not subject to the same-origin policy, so without this
	// check any page could open a socket to a localhost server and read the stream.
	s.upgrader = websocket.Upgrader{CheckOrigin: s.originAllowed}

	if cfg.APIServer.APIKey == "" {
		s.logger.Warn("API server starting with authentication disabled — " +
			"every endpoint is open to anything that can reach this port")
	}
	if wildcardOrigin {
		s.logger.Warn("Access-Control-Allow-Origin is '*' — any site the operator " +
			"visits can read this server's responses")
	}

	return s, nil
}

// originAllowed reports whether the request's Origin may talk to this server.
//
// A missing Origin header means a non-browser client (curl, an SDK, a server-side
// caller); those are not subject to the same-origin policy in the first place, and
// are gated by the API key instead.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if s.wildcardOrigin {
		return true
	}
	return s.allowedOrigins[strings.ToLower(strings.TrimSuffix(origin, "/"))]
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
		s.jobManager.Close()
		return shutdownWithGrace(server, 10*time.Second)
	case err := <-errCh:
		return err
	}
}

// --- Middleware ---

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Reaching here with no key configured means the operator explicitly passed
		// --insecure-no-auth; NewServer refuses to start otherwise.
		if s.apiKey == "" {
			next(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}

		// Constant-time: a byte-by-byte comparison leaks the key one character at a
		// time to anyone who can measure response latency.
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.apiKey)) != 1 {
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

// corsMiddleware emits CORS headers only for origins on the allowlist.
//
// It previously sent Access-Control-Allow-Origin: * unconditionally, which let any
// page the operator visited both call this server and read the response. Paired with
// the crawl endpoint, that is a working credential-theft chain, not a theoretical
// one: the page asks ScrapeGoat to fetch the cloud metadata endpoint and reads the
// answer back out of the CORS-permitted response.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		switch {
		case origin == "":
			// Not a browser request; nothing to negotiate.
		case s.wildcardOrigin:
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case s.allowedOrigins[strings.ToLower(strings.TrimSuffix(origin, "/"))]:
			w.Header().Set("Access-Control-Allow-Origin", origin)
			// The response varies by Origin, so caches must not serve one origin's
			// response to another.
			w.Header().Add("Vary", "Origin")
		default:
			// No ACAO header: the browser blocks the caller from reading the response.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}

		if origin != "" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
		}

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

// shutdownWithGrace stops srv, allowing up to d for in-flight requests to finish.
//
// The deadline is built on a fresh context rather than derived from the caller's.
// That is deliberate and is the whole point: a caller reaches shutdown *because*
// its context was cancelled, so a derived context would already be dead and
// Shutdown would return immediately — dropping the connections it was called to
// drain.
//
//nolint:contextcheck // a shutdown deadline must outlive the context that triggered it
func shutdownWithGrace(srv *http.Server, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return srv.Shutdown(ctx)
}

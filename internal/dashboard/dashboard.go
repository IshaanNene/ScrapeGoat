package dashboard

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// StatsProvider provides engine statistics.
type StatsProvider interface {
	GetStats() map[string]any
	GetState() string
}

// Dashboard serves the real-time web dashboard.
type Dashboard struct {
	port     int
	provider StatsProvider
	logger   *slog.Logger
}

// NewDashboard creates a new dashboard server.
func NewDashboard(port int, provider StatsProvider, logger *slog.Logger) *Dashboard {
	return &Dashboard{
		port:     port,
		provider: provider,
		logger:   logger.With("component", "dashboard"),
	}
}

// Start starts the dashboard server.
func (d *Dashboard) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", d.handleDashboard)
	mux.HandleFunc("/api/stats", d.handleAPIStats)

	addr := fmt.Sprintf(":%d", d.port)
	d.logger.Info("dashboard starting", "addr", addr)

	go func() {
		server := &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
		}
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			d.logger.Error("dashboard error", "error", err)
		}
	}()

	return nil
}

func (d *Dashboard) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(dashboardHTML))
}

func (d *Dashboard) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	stats := map[string]any{
		"timestamp": time.Now().Format(time.RFC3339),
	}
	if d.provider != nil {
		stats["state"] = d.provider.GetState()
		for k, v := range d.provider.GetStats() {
			stats[k] = v
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(stats)
}

package apiserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
)

func securityConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testutil.LoopbackConfig()
	cfg.APIServer = config.APIServerConfig{
		Enabled: true,
		Port:    0,
		DBPath:  filepath.Join(t.TempDir(), "test.db"),
		CORS:    true,
		RateRPS: 100,
	}
	return cfg
}

// TestNewServerFailsClosedWithoutAPIKey is the regression test for auth that was
// fail-open: an empty API key used to mean "let everybody in" rather than "refuse
// to start".
func TestNewServerFailsClosedWithoutAPIKey(t *testing.T) {
	cfg := securityConfig(t)

	_, err := NewServer(cfg, testLogger())
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("expected the server to refuse to start without a key, got %v", err)
	}
}

func TestNewServerStartsUnauthenticatedOnlyWithExplicitOptOut(t *testing.T) {
	cfg := securityConfig(t)
	cfg.APIServer.AllowNoAuth = true

	srv, err := NewServer(cfg, testLogger())
	if err != nil {
		t.Fatalf("explicit opt-out should be honoured: %v", err)
	}
	if srv.apiKey != "" {
		t.Error("expected no API key to be configured")
	}
}

func TestAuthRejectsWrongKey(t *testing.T) {
	srv := testServer(t)
	handler := srv.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{"correct key", "X-API-Key", "test-key-123", http.StatusOK},
		{"correct bearer", "Authorization", "Bearer test-key-123", http.StatusOK},
		{"wrong key", "X-API-Key", "wrong", http.StatusUnauthorized},
		{"empty key", "X-API-Key", "", http.StatusUnauthorized},
		{"prefix of key", "X-API-Key", "test-key-12", http.StatusUnauthorized},
		{"key plus suffix", "X-API-Key", "test-key-1234", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
			if tt.value != "" {
				req.Header.Set(tt.header, tt.value)
			}
			rec := httptest.NewRecorder()

			handler(rec, req)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// TestCORSDoesNotEchoArbitraryOrigins is the regression test for
// Access-Control-Allow-Origin: *, which let any page the operator visited both
// drive the crawl endpoint and read the response back.
func TestCORSDoesNotEchoArbitraryOrigins(t *testing.T) {
	srv := testServer(t) // no AllowedOrigins configured

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q for an unlisted origin, want no header", got)
	}
}

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	cfg := securityConfig(t)
	cfg.APIServer.APIKey = "k"
	cfg.APIServer.AllowedOrigins = []string{"https://app.example"}

	srv, err := NewServer(cfg, testLogger())
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	for _, tc := range []struct {
		origin string
		want   string
	}{
		{"https://app.example", "https://app.example"},
		{"https://app.example/", "https://app.example/"}, // trailing slash normalised on lookup
		{"https://evil.example", ""},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()

			srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.want {
				t.Errorf("ACAO = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWebSocketOriginCheck covers cross-site WebSocket hijacking: the upgrade is
// not covered by the same-origin policy, so CheckOrigin is the only thing standing
// between a malicious page and the job stream.
func TestWebSocketOriginCheck(t *testing.T) {
	cfg := securityConfig(t)
	cfg.APIServer.APIKey = "k"
	cfg.APIServer.AllowedOrigins = []string{"https://app.example"}

	srv, err := NewServer(cfg, testLogger())
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{"no origin (non-browser client)", "", true},
		{"allowed origin", "https://app.example", true},
		{"other origin", "https://evil.example", false},
		{"null origin", "null", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/ws/abc", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if got := srv.originAllowed(req); got != tt.want {
				t.Errorf("originAllowed(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}

func TestWildcardOriginStillHonoured(t *testing.T) {
	cfg := securityConfig(t)
	cfg.APIServer.APIKey = "k"
	cfg.APIServer.AllowedOrigins = []string{"*"}

	srv, err := NewServer(cfg, testLogger())
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://anything.example")
	rec := httptest.NewRecorder()

	srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
}

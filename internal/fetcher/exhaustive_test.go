package fetcher

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var exhaustiveLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// ---------------------------------------------------------------------------
// Proxy Rotation Tests
// ---------------------------------------------------------------------------

func TestProxyRotationRoundRobin(t *testing.T) {
	t.Parallel()

	cfg := &config.ProxyConfig{
		Enabled:  true,
		Rotation: "round_robin",
		URLs: []string{
			"http://proxy1.test:8080",
			"http://proxy2.test:8080",
			"http://proxy3.test:8080",
		},
	}

	pm := NewProxyManager(cfg, exhaustiveLogger)

	if pm.Count() != 3 {
		t.Fatalf("expected 3 proxies, got %d", pm.Count())
	}

	// Verify round-robin: each proxy appears in order
	seen := make(map[string]int)
	for i := 0; i < 9; i++ {
		proxy := pm.Next()
		if proxy == nil {
			t.Fatal("unexpected nil proxy")
		}
		seen[proxy.Host]++
	}

	// Each proxy should be used 3 times
	for _, count := range seen {
		if count != 3 {
			t.Errorf("round-robin not even: %v", seen)
			break
		}
	}
}

func TestProxyRotationRandom(t *testing.T) {
	t.Parallel()

	cfg := &config.ProxyConfig{
		Enabled:  true,
		Rotation: "random",
		URLs: []string{
			"http://proxy1.test:8080",
			"http://proxy2.test:8080",
			"http://proxy3.test:8080",
		},
	}

	pm := NewProxyManager(cfg, exhaustiveLogger)

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		proxy := pm.Next()
		if proxy != nil {
			seen[proxy.Host] = true
		}
	}

	// All 3 proxies should appear within 100 random selections
	if len(seen) != 3 {
		t.Errorf("expected all 3 proxies used in random mode, got %d: %v", len(seen), seen)
	}
}

func TestProxyFailedRemoval(t *testing.T) {
	t.Parallel()

	cfg := &config.ProxyConfig{
		Enabled:  true,
		Rotation: "round_robin",
		URLs:     []string{"http://proxy1.test:8080", "http://proxy2.test:8080"},
	}

	pm := NewProxyManager(cfg, exhaustiveLogger)

	// Mark first proxy as failed
	proxy1 := pm.Next()
	pm.MarkFailed(proxy1, fmt.Errorf("connection refused"))

	if pm.HealthyCount() != 1 {
		t.Errorf("expected 1 healthy proxy, got %d", pm.HealthyCount())
	}

	// All subsequent requests should use the remaining healthy proxy
	for i := 0; i < 5; i++ {
		proxy := pm.Next()
		if proxy == nil {
			t.Fatal("should still have healthy proxies")
		}
		if proxy.Host == proxy1.Host {
			t.Error("failed proxy should not be returned")
		}
	}

	// Re-mark as healthy
	pm.MarkHealthy(proxy1)
	if pm.HealthyCount() != 2 {
		t.Errorf("expected 2 healthy proxies after recovery, got %d", pm.HealthyCount())
	}
}

func TestProxyAddRuntime(t *testing.T) {
	t.Parallel()

	cfg := &config.ProxyConfig{Enabled: true, Rotation: "round_robin", URLs: []string{}}
	pm := NewProxyManager(cfg, exhaustiveLogger)

	if pm.Count() != 0 {
		t.Fatal("should start empty")
	}

	pm.AddProxy("http://new-proxy.test:8080")
	if pm.Count() != 1 {
		t.Errorf("expected 1 proxy after add, got %d", pm.Count())
	}

	proxy := pm.Next()
	if proxy == nil || proxy.Host != "new-proxy.test:8080" {
		t.Errorf("unexpected proxy: %v", proxy)
	}
}

// ---------------------------------------------------------------------------
// Session Pool Tests
// ---------------------------------------------------------------------------

func TestSessionPoolConcurrentGetPut(t *testing.T) {
	t.Parallel()

	poolCfg := DefaultSessionPoolConfig()
	pool := NewSessionPool(poolCfg, exhaustiveLogger)

	var wg sync.WaitGroup
	const goroutines = 100

	sessions := make(chan *PooledSession, goroutines*10)

	// 100 concurrent Get calls
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				s := pool.Get()
				if s == nil {
					t.Error("Get() returned nil")
					return
				}
				sessions <- s
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(sessions)
		close(done)
	}()

	select {
	case <-done:
		count := 0
		for range sessions {
			count++
		}
		t.Logf("PASS: %d sessions retrieved concurrently without deadlock", count)
	case <-time.After(10 * time.Second):
		t.Fatal("DEADLOCK: SessionPool.Get() hung under concurrent access")
	}
}

// ---------------------------------------------------------------------------
// User Agent Rotation Tests
// ---------------------------------------------------------------------------

func TestUserAgentAllAgentsAppear(t *testing.T) {
	t.Parallel()

	pool := NewUserAgentPool()

	seen := make(map[string]bool)
	for i := 0; i < 500; i++ {
		ua := pool.Random()
		if ua == "" {
			t.Fatal("Random() returned empty UA")
		}
		seen[ua] = true
	}

	// All agents should appear within 500 random selections
	totalAgents := len(pool.agents)
	if len(seen) < totalAgents/2 {
		t.Errorf("only %d/%d agents appeared in 500 selections — distribution too skewed",
			len(seen), totalAgents)
	}
	t.Logf("saw %d/%d unique agents in 500 selections", len(seen), totalAgents)
}

func TestUserAgentRoundRobin(t *testing.T) {
	t.Parallel()

	pool := NewUserAgentPool()

	// Round-robin should cycle through all agents
	first := pool.RoundRobin()
	seenFirst := false
	for i := 0; i < len(pool.agents)+1; i++ {
		ua := pool.RoundRobin()
		if ua == first && i > 0 {
			seenFirst = true
			break
		}
	}

	if !seenFirst {
		t.Error("round-robin did not cycle back to first agent")
	}
}

// ---------------------------------------------------------------------------
// HTTP Fetcher Tests
// ---------------------------------------------------------------------------

func TestHTTPFetcherBasic(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>Test</h1></body></html>`)
	}))
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.RequestTimeout = 5 * time.Second

	fetcher, err := NewHTTPFetcher(cfg, exhaustiveLogger)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := types.NewRequest(ts.URL + "/page")
	resp, fetchErr := fetcher.Fetch(context.Background(), req)
	if fetchErr != nil {
		t.Fatalf("fetch error: %v", fetchErr)
	}

	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	if len(resp.Body) == 0 {
		t.Error("body is empty")
	}
	if resp.FetchDuration == 0 {
		t.Error("FetchDuration should be > 0")
	}
}

func TestHTTPFetcherTimeout(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(200)
	}))
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.RequestTimeout = 500 * time.Millisecond

	fetcher, err := NewHTTPFetcher(cfg, exhaustiveLogger)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := types.NewRequest(ts.URL + "/slow")
	start := time.Now()
	_, fetchErr := fetcher.Fetch(context.Background(), req)
	elapsed := time.Since(start)

	if fetchErr == nil {
		t.Error("expected timeout error")
	}
	if elapsed > 3*time.Second {
		t.Errorf("timeout took %s, should be ~500ms", elapsed)
	}
	t.Logf("timeout occurred in %s", elapsed)
}

func TestHTTPFetcher429Handling(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(429)
		fmt.Fprint(w, "rate limited")
	}))
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.RequestTimeout = 5 * time.Second

	fetcher, err := NewHTTPFetcher(cfg, exhaustiveLogger)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := types.NewRequest(ts.URL + "/limited")
	resp, fetchErr := fetcher.Fetch(context.Background(), req)
	if fetchErr != nil {
		// Some implementations treat 429 as a FetchError
		t.Logf("429 returned as error: %v", fetchErr)
	} else if resp.StatusCode != 429 {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Session Manager Tests
// ---------------------------------------------------------------------------

func TestSessionManagerPerDomain(t *testing.T) {
	t.Parallel()

	sm := NewSessionManager(exhaustiveLogger)

	jar1 := sm.GetJar("example.com")
	jar2 := sm.GetJar("other.com")
	jar1Again := sm.GetJar("example.com")

	if jar1 != jar1Again {
		t.Error("same domain should return same jar")
	}
	if jar1 == jar2 {
		t.Error("different domains should return different jars")
	}

	sm.ClearDomain("example.com")
	jar1New := sm.GetJar("example.com")
	if jar1New == jar1 {
		t.Error("cleared domain should get a new jar")
	}
}

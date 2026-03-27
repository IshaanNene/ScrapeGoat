package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- DefaultConfig Tests ---

func TestDefaultConfigValues(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Engine.Concurrency != 10 {
		t.Errorf("expected concurrency 10, got %d", cfg.Engine.Concurrency)
	}
	if cfg.Engine.MaxDepth != 5 {
		t.Errorf("expected max_depth 5, got %d", cfg.Engine.MaxDepth)
	}
	if cfg.Engine.RequestTimeout != 30*time.Second {
		t.Errorf("expected request_timeout 30s, got %v", cfg.Engine.RequestTimeout)
	}
	if cfg.Engine.PolitenessDelay != 1*time.Second {
		t.Errorf("expected politeness_delay 1s, got %v", cfg.Engine.PolitenessDelay)
	}
	if !cfg.Engine.RespectRobotsTxt {
		t.Error("expected respect_robots_txt true")
	}
	if cfg.Engine.MaxRetries != 3 {
		t.Errorf("expected max_retries 3, got %d", cfg.Engine.MaxRetries)
	}
	if len(cfg.Engine.UserAgents) == 0 {
		t.Error("expected at least one default user agent")
	}
}

func TestDefaultFetcherConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Fetcher.Type != "http" {
		t.Errorf("expected fetcher type 'http', got %q", cfg.Fetcher.Type)
	}
	if !cfg.Fetcher.FollowRedirects {
		t.Error("expected follow_redirects true")
	}
	if cfg.Fetcher.MaxRedirects != 10 {
		t.Errorf("expected max_redirects 10, got %d", cfg.Fetcher.MaxRedirects)
	}
	if cfg.Fetcher.MaxBodySize != 10*1024*1024 {
		t.Errorf("expected max_body_size 10MB, got %d", cfg.Fetcher.MaxBodySize)
	}
}

func TestDefaultStorageConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Storage.Type != "json" {
		t.Errorf("expected storage type 'json', got %q", cfg.Storage.Type)
	}
	if cfg.Storage.OutputPath != "./output" {
		t.Errorf("expected output_path './output', got %q", cfg.Storage.OutputPath)
	}
}

func TestDefaultProxyConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Proxy.Enabled {
		t.Error("expected proxy disabled by default")
	}
	if cfg.Proxy.Rotation != "round_robin" {
		t.Errorf("expected rotation 'round_robin', got %q", cfg.Proxy.Rotation)
	}
}

func TestDefaultMetricsConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Metrics.Enabled {
		t.Error("expected metrics disabled by default")
	}
	if cfg.Metrics.Port != 9090 {
		t.Errorf("expected metrics port 9090, got %d", cfg.Metrics.Port)
	}
}

// --- YAML Loading Tests ---

func TestLoadYAMLConfig(t *testing.T) {
	// Create a temporary YAML config file
	yamlContent := `
engine:
  concurrency: 20
  max_depth: 3
  politeness_delay: 500ms
  respect_robots_txt: false

fetcher:
  type: http
  max_body_size: 5242880

storage:
  type: jsonl
  output_path: ./custom_output
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Engine.Concurrency != 20 {
		t.Errorf("expected concurrency 20, got %d", cfg.Engine.Concurrency)
	}
	if cfg.Engine.MaxDepth != 3 {
		t.Errorf("expected max_depth 3, got %d", cfg.Engine.MaxDepth)
	}
	if cfg.Storage.Type != "jsonl" {
		t.Errorf("expected storage type 'jsonl', got %q", cfg.Storage.Type)
	}
}

func TestLoadNonExistentConfig(t *testing.T) {
	// Loading a non-existent file should return defaults or an error
	// depending on implementation — just ensure it doesn't panic
	cfg, err := Load("/nonexistent/path/config.yaml")
	if err != nil && cfg == nil {
		// Expected: either returns error or falls back to defaults
		return
	}
	if cfg != nil {
		// If it returns a config, it should have defaults
		if cfg.Engine.Concurrency <= 0 {
			t.Error("expected positive concurrency from default config")
		}
	}
}

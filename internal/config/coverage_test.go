package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")

	yaml := `engine:
  concurrency: 10
  max_depth: 5
  max_requests: 1000
  politeness_delay: 100ms
  request_timeout: 30s
  respect_robots_txt: true
  allowed_domains:
    - example.com
`
	_ = os.WriteFile(cfgFile, []byte(yaml), 0644)

	cfg, err := LoadFromFile(cfgFile)
	if err != nil {
		t.Fatalf("LoadFromFile error: %v", err)
	}
	if cfg.Engine.Concurrency != 10 {
		t.Errorf("concurrency=%d, want 10", cfg.Engine.Concurrency)
	}
	if cfg.Engine.MaxDepth != 5 {
		t.Errorf("max_depth=%d, want 5", cfg.Engine.MaxDepth)
	}
}

func TestLoadFromFileMissing(t *testing.T) {
	_, err := LoadFromFile("/tmp/does_not_exist_config_test.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestValidate(t *testing.T) {
	cfg := DefaultConfig()
	err := Validate(cfg)
	if err != nil {
		t.Errorf("default config should be valid: %v", err)
	}
}

func TestValidateBadConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Engine.Concurrency = 0
	err := Validate(cfg)
	if err == nil {
		t.Error("concurrency=0 should be invalid")
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		url   string
		valid bool
	}{
		{"https://example.com", true},
		{"http://example.com/path", true},
		{"not-a-url", false},
		{"", false},
	}
	for _, tt := range tests {
		err := ValidateURL(tt.url)
		if (err == nil) != tt.valid {
			t.Errorf("ValidateURL(%q) valid=%v, want %v", tt.url, err == nil, tt.valid)
		}
	}
}

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Load reads configuration from file, environment, and CLI flags.
// Priority (highest to lowest): CLI flags > env vars > config file > defaults.
func Load(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	v := viper.New()
	v.SetConfigType("yaml")

	// Set defaults from struct
	setDefaults(v, cfg)

	// Environment variable support
	v.SetEnvPrefix("SCRAPEGOAT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Load config file
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		// Search default locations
		v.SetConfigName("scrapegoat")
		v.AddConfigPath(".")
		v.AddConfigPath("./configs")
		home, err := os.UserHomeDir()
		if err == nil {
			v.AddConfigPath(filepath.Join(home, ".scrapegoat"))
		}
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && configPath != "" {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// Config file not found is okay if not explicitly specified
	}

	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return cfg, nil
}

// LoadFromFile reads configuration from a specific file path.
func LoadFromFile(path string) (*Config, error) {
	return Load(path)
}

// setDefaults registers default values in viper.
func setDefaults(v *viper.Viper, cfg *Config) {
	v.SetDefault("engine.concurrency", cfg.Engine.Concurrency)
	v.SetDefault("engine.max_depth", cfg.Engine.MaxDepth)
	v.SetDefault("engine.request_timeout", cfg.Engine.RequestTimeout)
	v.SetDefault("engine.politeness_delay", cfg.Engine.PolitenessDelay)
	v.SetDefault("engine.respect_robots_txt", cfg.Engine.RespectRobotsTxt)
	v.SetDefault("engine.max_retries", cfg.Engine.MaxRetries)
	v.SetDefault("engine.retry_delay", cfg.Engine.RetryDelay)
	v.SetDefault("engine.checkpoint_interval", cfg.Engine.CheckpointInterval)
	v.SetDefault("engine.user_agents", cfg.Engine.UserAgents)

	v.SetDefault("fetcher.type", cfg.Fetcher.Type)
	v.SetDefault("fetcher.follow_redirects", cfg.Fetcher.FollowRedirects)
	v.SetDefault("fetcher.max_redirects", cfg.Fetcher.MaxRedirects)
	v.SetDefault("fetcher.max_body_size", cfg.Fetcher.MaxBodySize)
	v.SetDefault("fetcher.idle_conn_timeout", cfg.Fetcher.IdleConnTimeout)
	v.SetDefault("fetcher.max_idle_conns", cfg.Fetcher.MaxIdleConns)

	v.SetDefault("proxy.enabled", cfg.Proxy.Enabled)
	v.SetDefault("proxy.rotation", cfg.Proxy.Rotation)
	v.SetDefault("proxy.health_check", cfg.Proxy.HealthCheck)
	v.SetDefault("proxy.rotate_on_fail", cfg.Proxy.RotateOnFail)

	v.SetDefault("storage.type", cfg.Storage.Type)
	v.SetDefault("storage.output_path", cfg.Storage.OutputPath)
	v.SetDefault("storage.batch_size", cfg.Storage.BatchSize)

	v.SetDefault("logging.level", cfg.Logging.Level)
	v.SetDefault("logging.format", cfg.Logging.Format)
	v.SetDefault("logging.output", cfg.Logging.Output)

	v.SetDefault("metrics.enabled", cfg.Metrics.Enabled)
	v.SetDefault("metrics.port", cfg.Metrics.Port)
	v.SetDefault("metrics.path", cfg.Metrics.Path)
}

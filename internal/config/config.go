package config

import (
	"time"
)

// Version is set at build time via ldflags.
var Version = "dev"

// Config is the root configuration for ScrapeGoat.
type Config struct {
	Engine      EngineConfig          `mapstructure:"engine"      yaml:"engine"`
	Fetcher     FetcherConfig         `mapstructure:"fetcher"     yaml:"fetcher"`
	Proxy       ProxyConfig           `mapstructure:"proxy"       yaml:"proxy"`
	Parser      ParserConfig          `mapstructure:"parser"      yaml:"parser"`
	Pipeline    PipelineConfig        `mapstructure:"pipeline"    yaml:"pipeline"`
	Storage     StorageConfig         `mapstructure:"storage"     yaml:"storage"`
	AI          AIConfig              `mapstructure:"ai"          yaml:"ai"`
	LLM         LLMConfig             `mapstructure:"llm"         yaml:"llm"`
	APIServer   APIServerConfig       `mapstructure:"api_server"  yaml:"api_server"`
	MCP         MCPConfig             `mapstructure:"mcp"         yaml:"mcp"`
	Logging     LoggingConfig         `mapstructure:"logging"     yaml:"logging"`
	Metrics     MetricsConfig         `mapstructure:"metrics"     yaml:"metrics"`
	Browser     BrowserConfig         `mapstructure:"browser"     yaml:"browser"`
	Distributed DistributedConfig     `mapstructure:"distributed" yaml:"distributed"`
	Middleware  MiddlewareGroupConfig `mapstructure:"middleware" yaml:"middleware"`
	Project     ProjectConfig         `mapstructure:"project"     yaml:"project"`
	Safety      SafetyConfig          `mapstructure:"safety"      yaml:"safety"`
}

// SafetyConfig controls the outbound network trust boundary — which URLs the
// crawler is willing to fetch. The defaults deny everything that is not a public
// http/https endpoint, because URLs reach the fetcher from callers the operator
// does not control (MCP clients, the REST API, links in crawled pages).
//
// See internal/safety and SECURITY.md for the threat model.
type SafetyConfig struct {
	// AllowedSchemes is the set of permitted URL schemes. Empty means http+https.
	AllowedSchemes []string `mapstructure:"allowed_schemes" yaml:"allowed_schemes"`

	// AllowPrivateAddresses permits fetching loopback, RFC1918, link-local, and
	// other non-public addresses. Turning this on makes the process an SSRF proxy
	// for anything that can hand it a URL — only enable it for a deliberate crawl
	// of an internal network.
	AllowPrivateAddresses bool `mapstructure:"allow_private_addresses" yaml:"allow_private_addresses"`

	// AllowedPrivateHosts exempts specific hostnames from the address checks
	// without opening up the whole private range.
	AllowedPrivateHosts []string `mapstructure:"allowed_private_hosts" yaml:"allowed_private_hosts"`
}

// EngineConfig controls the core crawler engine.
type EngineConfig struct {
	Concurrency     int           `mapstructure:"concurrency"          yaml:"concurrency"`
	MaxDepth        int           `mapstructure:"max_depth"            yaml:"max_depth"`
	RequestTimeout  time.Duration `mapstructure:"request_timeout"      yaml:"request_timeout"`
	PolitenessDelay time.Duration `mapstructure:"politeness_delay"     yaml:"politeness_delay"`

	// MaxThrottleSlots bounds how many per-domain rate limiters are retained.
	// The map used to grow one entry per domain with no eviction, which leaked
	// memory monotonically on a broad crawl. Zero uses the default.
	MaxThrottleSlots int `mapstructure:"max_throttle_slots" yaml:"max_throttle_slots"`

	RespectRobotsTxt bool          `mapstructure:"respect_robots_txt"   yaml:"respect_robots_txt"`
	MaxRetries       int           `mapstructure:"max_retries"          yaml:"max_retries"`
	RetryDelay       time.Duration `mapstructure:"retry_delay"          yaml:"retry_delay"`

	// CircuitBreakerThreshold is how many consecutive failures open a domain's
	// circuit. Zero disables the breaker.
	CircuitBreakerThreshold int `mapstructure:"circuit_breaker_threshold" yaml:"circuit_breaker_threshold"`

	// CircuitBreakerCooldown is how long an open circuit waits before allowing
	// a single probe through.
	CircuitBreakerCooldown time.Duration `mapstructure:"circuit_breaker_cooldown" yaml:"circuit_breaker_cooldown"`

	CheckpointInterval time.Duration `mapstructure:"checkpoint_interval"  yaml:"checkpoint_interval"`
	UserAgents         []string      `mapstructure:"user_agents"          yaml:"user_agents"`
	AllowedDomains     []string      `mapstructure:"allowed_domains"      yaml:"allowed_domains"`
	DisallowedDomains  []string      `mapstructure:"disallowed_domains"   yaml:"disallowed_domains"`
	AllowedURLPatterns []string      `mapstructure:"allowed_url_patterns" yaml:"allowed_url_patterns"`
	MaxRequests        int           `mapstructure:"max_requests"         yaml:"max_requests"`
	MaxItems           int           `mapstructure:"max_items"            yaml:"max_items"`
}

// FetcherConfig controls the request fetcher.
type FetcherConfig struct {
	Type            string        `mapstructure:"type"              yaml:"type"`
	FollowRedirects bool          `mapstructure:"follow_redirects"  yaml:"follow_redirects"`
	MaxRedirects    int           `mapstructure:"max_redirects"     yaml:"max_redirects"`
	MaxBodySize     int64         `mapstructure:"max_body_size"     yaml:"max_body_size"`
	TLSInsecure     bool          `mapstructure:"tls_insecure"      yaml:"tls_insecure"`
	IdleConnTimeout time.Duration `mapstructure:"idle_conn_timeout" yaml:"idle_conn_timeout"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"    yaml:"max_idle_conns"`
}

// ProxyConfig controls proxy rotation.
type ProxyConfig struct {
	Enabled      bool     `mapstructure:"enabled"       yaml:"enabled"`
	Rotation     string   `mapstructure:"rotation"      yaml:"rotation"`
	URLs         []string `mapstructure:"urls"           yaml:"urls"`
	HealthCheck  bool     `mapstructure:"health_check"   yaml:"health_check"`
	RotateOnFail bool     `mapstructure:"rotate_on_fail" yaml:"rotate_on_fail"`
}

// ParserConfig controls the parser.
type ParserConfig struct {
	AutoDetect bool        `mapstructure:"auto_detect" yaml:"auto_detect"`
	Rules      []ParseRule `mapstructure:"rules"       yaml:"rules"`
}

// ParseRule defines a single extraction rule.
type ParseRule struct {
	Name      string `mapstructure:"name"      yaml:"name"`
	Selector  string `mapstructure:"selector"  yaml:"selector"`
	Type      string `mapstructure:"type"      yaml:"type"` // css, xpath, regex
	Attribute string `mapstructure:"attribute" yaml:"attribute"`
	Pattern   string `mapstructure:"pattern"   yaml:"pattern"`
}

// PipelineConfig controls the processing pipeline.
type PipelineConfig struct {
	Middlewares []MiddlewareConfig `mapstructure:"middlewares" yaml:"middlewares"`
}

// MiddlewareConfig defines a single pipeline middleware.
type MiddlewareConfig struct {
	Name    string         `mapstructure:"name"    yaml:"name"`
	Type    string         `mapstructure:"type"    yaml:"type"`
	Options map[string]any `mapstructure:"options" yaml:"options"`
}

// StorageConfig controls output/storage.
type StorageConfig struct {
	Type       string `mapstructure:"type"        yaml:"type"`
	OutputPath string `mapstructure:"output_path" yaml:"output_path"`
	BatchSize  int    `mapstructure:"batch_size"  yaml:"batch_size"`
}

// AIConfig controls LLM integration.
type AIConfig struct {
	Enabled  bool   `mapstructure:"enabled"   yaml:"enabled"`
	Provider string `mapstructure:"provider"  yaml:"provider"`
	Model    string `mapstructure:"model"     yaml:"model"`
	Endpoint string `mapstructure:"endpoint"  yaml:"endpoint"`
}

// LoggingConfig controls logging behavior.
type LoggingConfig struct {
	Level  string `mapstructure:"level"  yaml:"level"`
	Format string `mapstructure:"format" yaml:"format"`
	Output string `mapstructure:"output" yaml:"output"`
}

// MetricsConfig controls Prometheus metrics.
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled" yaml:"enabled"`
	Port    int    `mapstructure:"port"    yaml:"port"`
	Path    string `mapstructure:"path"    yaml:"path"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Engine: EngineConfig{
			Concurrency:             10,
			MaxDepth:                5,
			RequestTimeout:          30 * time.Second,
			PolitenessDelay:         1 * time.Second,
			MaxThrottleSlots:        10_000,
			RespectRobotsTxt:        true,
			MaxRetries:              3,
			RetryDelay:              2 * time.Second,
			CircuitBreakerThreshold: 5,
			CircuitBreakerCooldown:  30 * time.Second,
			CheckpointInterval:      60 * time.Second,
			UserAgents: []string{
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			},
		},
		Fetcher: FetcherConfig{
			Type:            "http",
			FollowRedirects: true,
			MaxRedirects:    10,
			MaxBodySize:     10 * 1024 * 1024, // 10MB
			IdleConnTimeout: 90 * time.Second,
			MaxIdleConns:    100,
		},
		Proxy: ProxyConfig{
			Enabled:      false,
			Rotation:     "round_robin",
			HealthCheck:  true,
			RotateOnFail: true,
		},
		Safety: SafetyConfig{
			AllowedSchemes:        []string{"http", "https"},
			AllowPrivateAddresses: false,
		},
		Parser: ParserConfig{
			AutoDetect: true,
		},
		Storage: StorageConfig{
			Type:       "json",
			OutputPath: "./output",
			BatchSize:  100,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
			Output: "stderr",
		},
		Metrics: MetricsConfig{
			Enabled: false,
			Port:    9090,
			Path:    "/metrics",
		},
		Browser: BrowserConfig{
			Render:      false,
			BrowserType: "chromium",
			Headless:    true,
			WaitTime:    3 * time.Second,
		},
		Distributed: DistributedConfig{
			Enabled:    false,
			MasterAddr: ":8081",
			RedisAddr:  "localhost:6379",
		},
	}
}

// BrowserConfig controls headless browser rendering.
type BrowserConfig struct {
	Render      bool          `mapstructure:"render"       yaml:"render"`
	BrowserType string        `mapstructure:"browser_type" yaml:"browser_type"`
	Headless    bool          `mapstructure:"headless"     yaml:"headless"`
	WaitTime    time.Duration `mapstructure:"wait_time"    yaml:"wait_time"`
}

// DistributedConfig controls distributed crawling.
type DistributedConfig struct {
	Enabled    bool   `mapstructure:"enabled"     yaml:"enabled"`
	MasterAddr string `mapstructure:"master_addr" yaml:"master_addr"`
	RedisAddr  string `mapstructure:"redis_addr"  yaml:"redis_addr"`
	RedisDB    int    `mapstructure:"redis_db"    yaml:"redis_db"`
	RedisKey   string `mapstructure:"redis_key"   yaml:"redis_key"`
}

// MiddlewareGroupConfig groups request and pipeline middleware configs.
type MiddlewareGroupConfig struct {
	Request []RequestMiddlewareConfig `mapstructure:"request" yaml:"request"`
}

// RequestMiddlewareConfig configures a single request middleware.
type RequestMiddlewareConfig struct {
	Name    string         `mapstructure:"name"    yaml:"name"`
	Enabled bool           `mapstructure:"enabled" yaml:"enabled"`
	Options map[string]any `mapstructure:"options" yaml:"options"`
}

// ProjectConfig holds project-level configuration.
type ProjectConfig struct {
	Name      string   `mapstructure:"name"       yaml:"name"`
	Version   string   `mapstructure:"version"    yaml:"version"`
	StartURLs []string `mapstructure:"start_urls" yaml:"start_urls"`
}

// LLMConfig configures the LLM extraction engine.
type LLMConfig struct {
	Enabled   bool          `mapstructure:"enabled"      yaml:"enabled"`
	Provider  string        `mapstructure:"provider"     yaml:"provider"` // openai, anthropic, ollama
	Model     string        `mapstructure:"model"        yaml:"model"`
	APIKey    string        `mapstructure:"api_key"      yaml:"api_key"`
	Endpoint  string        `mapstructure:"endpoint"     yaml:"endpoint"`
	CachePath string        `mapstructure:"cache_path"   yaml:"cache_path"`
	CacheTTL  time.Duration `mapstructure:"cache_ttl"    yaml:"cache_ttl"`
	MaxTokens int           `mapstructure:"max_tokens"   yaml:"max_tokens"`
}

// APIServerConfig configures the REST/WebSocket API server.
type APIServerConfig struct {
	Enabled bool   `mapstructure:"enabled"   yaml:"enabled"`
	Port    int    `mapstructure:"port"      yaml:"port"`
	APIKey  string `mapstructure:"api_key"   yaml:"api_key"`
	DBPath  string `mapstructure:"db_path"   yaml:"db_path"`
	CORS    bool   `mapstructure:"cors"      yaml:"cors"`
	RateRPS int    `mapstructure:"rate_rps"  yaml:"rate_rps"` // requests per second per key

	// AllowNoAuth permits starting without an API key. The server otherwise
	// refuses to start unauthenticated, because an empty key used to mean "let
	// everyone in" — and an unauthenticated crawl endpoint on localhost is
	// reachable from any web page the operator happens to visit.
	AllowNoAuth bool `mapstructure:"allow_no_auth" yaml:"allow_no_auth"`

	// AllowedOrigins lists the origins permitted by CORS and accepted on the
	// WebSocket upgrade. Empty means same-origin only: no Access-Control-Allow-Origin
	// header is emitted and cross-origin WebSocket upgrades are refused.
	//
	// "*" is honoured but makes every response readable by any site the operator
	// visits; combined with a crawl endpoint that is an SSRF primitive, that is a
	// drive-by credential-theft chain. Set it only for a deliberately public,
	// authenticated deployment.
	AllowedOrigins []string `mapstructure:"allowed_origins" yaml:"allowed_origins"`
}

// MCPConfig configures the MCP server.
type MCPConfig struct {
	Transport string `mapstructure:"transport" yaml:"transport"` // stdio or http
	Port      int    `mapstructure:"port"      yaml:"port"`
	APIKey    string `mapstructure:"api_key"   yaml:"api_key"`
}

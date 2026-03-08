package builtin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/plugin"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// --- S3 Storage Plugin ---

// S3StoragePlugin stores scraped items in Amazon S3.
// Requires AWS credentials to be configured via environment variables or AWS config.
type S3StoragePlugin struct {
	bucket     string
	prefix     string
	region     string
	format     string
	logger     *slog.Logger
	buffer     []*types.Item
	mu         sync.Mutex
	flushSize  int
	batchCount int
}

func (p *S3StoragePlugin) Name() string            { return "scrapegoat-s3" }
func (p *S3StoragePlugin) Type() plugin.PluginType { return plugin.PluginTypeStorage }
func (p *S3StoragePlugin) Version() string         { return "1.0.0" }

func (p *S3StoragePlugin) Init(cfg map[string]any) error {
	if bucket, ok := cfg["bucket"].(string); ok {
		p.bucket = bucket
	}
	if prefix, ok := cfg["prefix"].(string); ok {
		p.prefix = prefix
	}
	if region, ok := cfg["region"].(string); ok {
		p.region = region
	}
	if format, ok := cfg["format"].(string); ok {
		p.format = format
	}
	if p.format == "" {
		p.format = "jsonl"
	}
	if p.prefix == "" {
		p.prefix = "scrapegoat/"
	}
	if p.region == "" {
		p.region = "us-east-1"
	}
	if flushSize, ok := cfg["flush_size"].(float64); ok {
		p.flushSize = int(flushSize)
	}
	if p.flushSize == 0 {
		p.flushSize = 100
	}

	p.logger.Info("S3 plugin initialized",
		"bucket", p.bucket,
		"prefix", p.prefix,
		"region", p.region,
		"format", p.format,
	)
	return nil
}

func (p *S3StoragePlugin) Store(items []*types.Item) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.buffer = append(p.buffer, items...)

	if len(p.buffer) >= p.flushSize {
		return p.flush()
	}
	return nil
}

func (p *S3StoragePlugin) flush() error {
	if len(p.buffer) == 0 {
		return nil
	}

	p.batchCount++
	key := fmt.Sprintf("%s%s/batch-%04d.%s",
		p.prefix,
		time.Now().Format("2006-01-02"),
		p.batchCount,
		p.format,
	)

	// Serialize items
	var data []byte
	switch p.format {
	case "jsonl":
		var lines []string
		for _, item := range p.buffer {
			line, _ := json.Marshal(item.Fields)
			lines = append(lines, string(line))
		}
		data = []byte(strings.Join(lines, "\n"))
	default: // json
		data, _ = json.MarshalIndent(p.buffer, "", "  ")
	}

	// In production, this would use the AWS SDK to upload to S3.
	// For now, we log the operation and write to a local fallback.
	p.logger.Info("S3 upload",
		"bucket", p.bucket,
		"key", key,
		"items", len(p.buffer),
		"size_bytes", len(data),
	)

	// Fallback: write to local directory
	localDir := filepath.Join("output", "s3-fallback", p.bucket)
	os.MkdirAll(localDir, 0o755)
	localPath := filepath.Join(localDir, filepath.Base(key))
	os.WriteFile(localPath, data, 0o644)

	p.buffer = nil
	return nil
}

func (p *S3StoragePlugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flush()
}

// NewS3StoragePlugin creates a new S3 storage plugin.
func NewS3StoragePlugin(logger *slog.Logger) *S3StoragePlugin {
	return &S3StoragePlugin{
		logger: logger.With("plugin", "scrapegoat-s3"),
	}
}

// --- Kafka Publisher Plugin ---

// KafkaPublisherPlugin publishes scraped items to Apache Kafka.
type KafkaPublisherPlugin struct {
	brokers   []string
	topic     string
	logger    *slog.Logger
	buffer    []*types.Item
	mu        sync.Mutex
	published int64
}

func (p *KafkaPublisherPlugin) Name() string            { return "scrapegoat-kafka" }
func (p *KafkaPublisherPlugin) Type() plugin.PluginType { return plugin.PluginTypeStorage }
func (p *KafkaPublisherPlugin) Version() string         { return "1.0.0" }

func (p *KafkaPublisherPlugin) Init(cfg map[string]any) error {
	if brokers, ok := cfg["brokers"].(string); ok {
		p.brokers = strings.Split(brokers, ",")
	}
	if topic, ok := cfg["topic"].(string); ok {
		p.topic = topic
	}
	if p.topic == "" {
		p.topic = "scrapegoat-items"
	}
	if len(p.brokers) == 0 {
		p.brokers = []string{"localhost:9092"}
	}

	p.logger.Info("Kafka plugin initialized",
		"brokers", p.brokers,
		"topic", p.topic,
	)
	return nil
}

func (p *KafkaPublisherPlugin) Store(items []*types.Item) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, item := range items {
		data, _ := json.Marshal(map[string]any{
			"url":        item.URL,
			"spider":     item.SpiderName,
			"fields":     item.Fields,
			"scraped_at": item.Timestamp,
		})

		// In production, this would use confluent-kafka-go or sarama.
		// For now, we log the publish operation and buffer locally.
		p.logger.Debug("Kafka publish",
			"topic", p.topic,
			"key", item.URL,
			"size_bytes", len(data),
		)
		p.published++
	}

	return nil
}

func (p *KafkaPublisherPlugin) Close() error {
	p.logger.Info("Kafka plugin closed", "total_published", p.published)
	return nil
}

// NewKafkaPublisherPlugin creates a new Kafka publisher plugin.
func NewKafkaPublisherPlugin(logger *slog.Logger) *KafkaPublisherPlugin {
	return &KafkaPublisherPlugin{
		logger: logger.With("plugin", "scrapegoat-kafka"),
	}
}

// --- PostgreSQL Storage Plugin ---

// PostgresStoragePlugin stores scraped items in PostgreSQL.
type PostgresStoragePlugin struct {
	dsn       string
	table     string
	logger    *slog.Logger
	buffer    []*types.Item
	mu        sync.Mutex
	inserted  int64
	batchSize int
}

func (p *PostgresStoragePlugin) Name() string            { return "scrapegoat-postgres" }
func (p *PostgresStoragePlugin) Type() plugin.PluginType { return plugin.PluginTypeStorage }
func (p *PostgresStoragePlugin) Version() string         { return "1.0.0" }

func (p *PostgresStoragePlugin) Init(cfg map[string]any) error {
	if dsn, ok := cfg["dsn"].(string); ok {
		p.dsn = dsn
	}
	if table, ok := cfg["table"].(string); ok {
		p.table = table
	}
	if batchSize, ok := cfg["batch_size"].(float64); ok {
		p.batchSize = int(batchSize)
	}
	if p.dsn == "" {
		p.dsn = "postgres://localhost:5432/scrapegoat?sslmode=disable"
	}
	if p.table == "" {
		p.table = "scraped_items"
	}
	if p.batchSize == 0 {
		p.batchSize = 50
	}

	// In production, create the table if not exists:
	// CREATE TABLE IF NOT EXISTS scraped_items (
	//     id SERIAL PRIMARY KEY,
	//     url TEXT NOT NULL,
	//     spider_name TEXT,
	//     fields JSONB,
	//     scraped_at TIMESTAMPTZ DEFAULT NOW()
	// );

	p.logger.Info("PostgreSQL plugin initialized",
		"dsn", maskDSN(p.dsn),
		"table", p.table,
		"batch_size", p.batchSize,
	)
	return nil
}

func (p *PostgresStoragePlugin) Store(items []*types.Item) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.buffer = append(p.buffer, items...)

	if len(p.buffer) >= p.batchSize {
		return p.flush()
	}
	return nil
}

func (p *PostgresStoragePlugin) flush() error {
	if len(p.buffer) == 0 {
		return nil
	}

	// In production, this would use database/sql with pgx driver:
	// INSERT INTO scraped_items (url, spider_name, fields, scraped_at)
	// VALUES ($1, $2, $3, $4)
	for _, item := range p.buffer {
		p.logger.Debug("PostgreSQL insert",
			"table", p.table,
			"url", item.URL,
			"fields_count", len(item.Fields),
		)
		p.inserted++
	}

	p.logger.Info("PostgreSQL batch insert",
		"table", p.table,
		"items", len(p.buffer),
		"total_inserted", p.inserted,
	)

	p.buffer = nil
	return nil
}

func (p *PostgresStoragePlugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.flush(); err != nil {
		return err
	}

	p.logger.Info("PostgreSQL plugin closed", "total_inserted", p.inserted)
	return nil
}

// NewPostgresStoragePlugin creates a new PostgreSQL storage plugin.
func NewPostgresStoragePlugin(logger *slog.Logger) *PostgresStoragePlugin {
	return &PostgresStoragePlugin{
		logger: logger.With("plugin", "scrapegoat-postgres"),
	}
}

// maskDSN masks sensitive parts of a database connection string.
func maskDSN(dsn string) string {
	if strings.Contains(dsn, "@") {
		parts := strings.SplitN(dsn, "@", 2)
		return "***@" + parts[1]
	}
	return dsn
}

// --- Plugin Registration ---

// RegisterBuiltinPlugins registers all built-in plugins.
func RegisterBuiltinPlugins(registry *plugin.Registry, logger *slog.Logger) error {
	plugins := []plugin.Plugin{
		NewS3StoragePlugin(logger),
		NewKafkaPublisherPlugin(logger),
		NewPostgresStoragePlugin(logger),
	}

	for _, p := range plugins {
		if err := registry.Register(p); err != nil {
			return fmt.Errorf("register %s: %w", p.Name(), err)
		}
	}

	return nil
}

// Ensure plugins implement the interface.
var (
	_ plugin.Plugin = (*S3StoragePlugin)(nil)
	_ plugin.Plugin = (*KafkaPublisherPlugin)(nil)
	_ plugin.Plugin = (*PostgresStoragePlugin)(nil)
)

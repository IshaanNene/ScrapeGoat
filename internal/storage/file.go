package storage

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// --- JSON Storage ---

// JSONStorage writes items as a JSON array to a file.
type JSONStorage struct {
	path   string
	items  []*types.Item
	mu     sync.Mutex
	logger *slog.Logger
}

// NewJSONStorage creates a new JSON file storage.
func NewJSONStorage(outputPath string, logger *slog.Logger) (*JSONStorage, error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	return &JSONStorage{
		path:   outputPath,
		items:  make([]*types.Item, 0),
		logger: logger.With("component", "json_storage"),
	}, nil
}

func (s *JSONStorage) Name() string { return "json" }

func (s *JSONStorage) Store(items []*types.Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, items...)
	s.logger.Debug("items buffered", "count", len(items), "total", len(s.items))
	return nil
}

func (s *JSONStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Create(s.path)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer f.Close()

	// Build output as array of field maps
	output := make([]map[string]any, len(s.items))
	for i, item := range s.items {
		entry := make(map[string]any, len(item.Fields)+3)
		entry["_url"] = item.URL
		entry["_timestamp"] = item.Timestamp
		if item.SpiderName != "" {
			entry["_spider"] = item.SpiderName
		}
		for k, v := range item.Fields {
			entry[k] = v
		}
		output[i] = entry
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}

	s.logger.Info("JSON written", "path", s.path, "items", len(s.items))
	return nil
}

// --- JSONL Storage ---

// JSONLStorage writes items as newline-delimited JSON (one object per line).
type JSONLStorage struct {
	path   string
	file   *os.File
	enc    *json.Encoder
	mu     sync.Mutex
	count  int
	logger *slog.Logger
}

// NewJSONLStorage creates a new JSONL file storage (streaming writes).
func NewJSONLStorage(outputPath string, logger *slog.Logger) (*JSONLStorage, error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}

	return &JSONLStorage{
		path:   outputPath,
		file:   f,
		enc:    json.NewEncoder(f),
		logger: logger.With("component", "jsonl_storage"),
	}, nil
}

func (s *JSONLStorage) Name() string { return "jsonl" }

func (s *JSONLStorage) Store(items []*types.Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, item := range items {
		entry := make(map[string]any, len(item.Fields)+3)
		entry["_url"] = item.URL
		entry["_timestamp"] = item.Timestamp
		if item.SpiderName != "" {
			entry["_spider"] = item.SpiderName
		}
		for k, v := range item.Fields {
			entry[k] = v
		}

		if err := s.enc.Encode(entry); err != nil {
			return fmt.Errorf("encode JSONL: %w", err)
		}
		s.count++
	}
	return nil
}

func (s *JSONLStorage) Close() error {
	s.logger.Info("JSONL written", "path", s.path, "items", s.count)
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// --- CSV Storage ---

// CSVStorage writes items as CSV rows.
type CSVStorage struct {
	path    string
	file    *os.File
	writer  *csv.Writer
	headers []string
	mu      sync.Mutex
	count   int
	logger  *slog.Logger
}

// NewCSVStorage creates a new CSV file storage.
func NewCSVStorage(outputPath string, logger *slog.Logger) (*CSVStorage, error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}

	return &CSVStorage{
		path:   outputPath,
		file:   f,
		writer: csv.NewWriter(f),
		logger: logger.With("component", "csv_storage"),
	}, nil
}

func (s *CSVStorage) Name() string { return "csv" }

func (s *CSVStorage) Store(items []*types.Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, item := range items {
		flat := item.ToFlatMap()

		// Detect headers on first item
		if s.headers == nil {
			s.headers = make([]string, 0, len(flat))
			for k := range flat {
				s.headers = append(s.headers, k)
			}
			sort.Strings(s.headers)

			// Write header row
			if err := s.writer.Write(s.headers); err != nil {
				return fmt.Errorf("write CSV header: %w", err)
			}
		}

		// Write row
		row := make([]string, len(s.headers))
		for i, h := range s.headers {
			row[i] = flat[h]
		}
		if err := s.writer.Write(row); err != nil {
			return fmt.Errorf("write CSV row: %w", err)
		}
		s.count++
	}

	s.writer.Flush()
	return s.writer.Error()
}

func (s *CSVStorage) Close() error {
	s.logger.Info("CSV written", "path", s.path, "items", s.count)
	if s.writer != nil {
		s.writer.Flush()
	}
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// NewFileStorage creates the appropriate file-based storage by type.
func NewFileStorage(storageType, outputDir string, logger *slog.Logger) (Storage, error) {
	switch storageType {
	case "json":
		return NewJSONStorage(filepath.Join(outputDir, "results.json"), logger)
	case "jsonl":
		return NewJSONLStorage(filepath.Join(outputDir, "results.jsonl"), logger)
	case "csv":
		return NewCSVStorage(filepath.Join(outputDir, "results.csv"), logger)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", storageType)
	}
}

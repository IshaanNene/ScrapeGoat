package storage

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

// --- JSON Storage ---

func TestJSONStorage(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStorage("json", dir, testLogger)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	items := []*types.Item{
		makeItem("https://example.com/1", "title", "Page 1"),
		makeItem("https://example.com/2", "title", "Page 2"),
	}

	if err := store.Store(items); err != nil {
		t.Fatalf("store error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}

	// Read and validate JSON
	data, err := os.ReadFile(filepath.Join(dir, "results.json"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	var results []map[string]any
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

// --- JSONL Storage ---

func TestJSONLStorage(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStorage("jsonl", dir, testLogger)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	items := []*types.Item{
		makeItem("https://example.com/1", "title", "Page 1"),
		makeItem("https://example.com/2", "title", "Page 2"),
		makeItem("https://example.com/3", "title", "Page 3"),
	}

	if err := store.Store(items); err != nil {
		t.Fatalf("store error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}

	// Read and validate JSONL (one JSON object per line)
	data, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}

	// Each line should be valid JSON
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

// --- CSV Storage ---

func TestCSVStorage(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStorage("csv", dir, testLogger)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	items := []*types.Item{
		makeItem("https://example.com/1", "title", "Page 1"),
		makeItem("https://example.com/2", "title", "Page 2"),
	}

	if err := store.Store(items); err != nil {
		t.Fatalf("store error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}

	// Read and validate CSV
	f, err := os.Open(filepath.Join(dir, "results.csv"))
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}

	// Header + 2 data rows
	if len(records) < 2 {
		t.Errorf("expected at least 2 CSV records (header + data), got %d", len(records))
	}
}

// --- Storage Name ---

func TestStorageName(t *testing.T) {
	dir := t.TempDir()
	for _, format := range []string{"json", "jsonl", "csv"} {
		store, err := NewFileStorage(format, dir, testLogger)
		if err != nil {
			t.Fatalf("create %s storage: %v", format, err)
		}
		name := store.Name()
		if name == "" {
			t.Errorf("%s storage name should not be empty", format)
		}
		store.Close()
	}
}

// --- Helpers ---

func makeItem(url, key, value string) *types.Item {
	item := types.NewItem(url)
	item.Set(key, value)
	return item
}

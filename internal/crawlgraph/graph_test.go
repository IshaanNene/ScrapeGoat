package crawlgraph

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testGraph(t *testing.T) *CrawlGraph {
	t.Helper()
	dir := t.TempDir()
	g, err := New(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatalf("create graph: %v", err)
	}
	return g
}

func TestGraph_AddAndGetNodes(t *testing.T) {
	g := testGraph(t)
	defer g.Close()

	g.StartSession("https://example.com")

	nodes := []Node{
		{URL: "https://example.com", StatusCode: 200, Depth: 0, DiscoveredAt: time.Now()},
		{URL: "https://example.com/about", StatusCode: 200, Depth: 1, DiscoveredAt: time.Now()},
		{URL: "https://example.com/404", StatusCode: 404, Depth: 1, Error: "not found", DiscoveredAt: time.Now()},
	}

	for _, n := range nodes {
		if err := g.AddNode(n); err != nil {
			t.Fatalf("add node: %v", err)
		}
	}

	all, err := g.GetNodes()
	if err != nil {
		t.Fatalf("get nodes: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("node count = %d, want 3", len(all))
	}
}

func TestGraph_FailedNodes(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")

	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, DiscoveredAt: time.Now()})
	g.AddNode(Node{URL: "https://example.com/err", StatusCode: 500, Error: "server error", DiscoveredAt: time.Now()})
	g.AddNode(Node{URL: "https://example.com/404", StatusCode: 404, DiscoveredAt: time.Now()})

	failed, err := g.GetFailedNodes()
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if len(failed) != 2 {
		t.Errorf("failed count = %d, want 2", len(failed))
	}
}

func TestGraph_Edges(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")

	g.AddEdge(Edge{FromURL: "https://example.com", ToURL: "https://example.com/about", LinkText: "About"})
	g.AddEdge(Edge{FromURL: "https://example.com", ToURL: "https://example.com/contact"})

	edges, err := g.GetEdges()
	if err != nil {
		t.Fatalf("get edges: %v", err)
	}
	if len(edges) != 2 {
		t.Errorf("edge count = %d, want 2", len(edges))
	}
}

func TestGraph_Stats(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")

	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, Depth: 0, ItemCount: 5, CrawlTimeMs: 100, DiscoveredAt: time.Now()})
	g.AddNode(Node{URL: "https://example.com/a", StatusCode: 200, Depth: 1, ItemCount: 3, CrawlTimeMs: 200, DiscoveredAt: time.Now()})
	g.AddNode(Node{URL: "https://example.com/b", StatusCode: 500, Depth: 1, Error: "err", CrawlTimeMs: 50, DiscoveredAt: time.Now()})

	stats, err := g.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.NodeCount != 3 {
		t.Errorf("node count = %d, want 3", stats.NodeCount)
	}
	if stats.SuccessCount != 2 {
		t.Errorf("success = %d, want 2", stats.SuccessCount)
	}
	if stats.ErrorCount != 1 {
		t.Errorf("errors = %d, want 1", stats.ErrorCount)
	}
	if stats.MaxDepth != 1 {
		t.Errorf("max depth = %d, want 1", stats.MaxDepth)
	}
	if stats.TotalItems != 8 {
		t.Errorf("total items = %d, want 8", stats.TotalItems)
	}
}

func TestGraph_ExportJSON(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")
	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, DiscoveredAt: time.Now()})

	data, err := g.ExportJSON()
	if err != nil {
		t.Fatalf("export json: %v", err)
	}
	if !strings.Contains(string(data), "example.com") {
		t.Error("JSON export should contain the URL")
	}
}

func TestGraph_ExportDOT(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")
	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, DiscoveredAt: time.Now()})
	g.AddNode(Node{URL: "https://example.com/a", StatusCode: 200, DiscoveredAt: time.Now()})
	g.AddEdge(Edge{FromURL: "https://example.com", ToURL: "https://example.com/a"})

	dot, err := g.ExportDOT()
	if err != nil {
		t.Fatalf("export dot: %v", err)
	}
	if !strings.Contains(dot, "digraph") {
		t.Error("DOT export should contain 'digraph'")
	}
	if !strings.Contains(dot, "->") {
		t.Error("DOT export should contain edges")
	}
}

func TestGraph_ExportMermaid(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")
	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, DiscoveredAt: time.Now()})

	mermaid, err := g.ExportMermaid()
	if err != nil {
		t.Fatalf("export mermaid: %v", err)
	}
	if !strings.Contains(mermaid, "graph TD") {
		t.Error("Mermaid export should contain 'graph TD'")
	}
}

func TestGraph_ExportCSV(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")
	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, DiscoveredAt: time.Now()})

	csv, err := g.ExportCSV()
	if err != nil {
		t.Fatalf("export csv: %v", err)
	}
	if !strings.Contains(csv, "url,status_code") {
		t.Error("CSV should have header")
	}
}

func TestReplay_Errors(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")

	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, DiscoveredAt: time.Now()})
	g.AddNode(Node{URL: "https://example.com/broken", StatusCode: 500, Error: "err", DiscoveredAt: time.Now()})

	urls, err := GetReplayURLs(g, ReplayConfig{Strategy: ReplayErrors}, testLogger())
	if err != nil {
		t.Fatalf("replay errors: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("replay urls = %d, want 1", len(urls))
	}
	if urls[0] != "https://example.com/broken" {
		t.Errorf("url = %s, want https://example.com/broken", urls[0])
	}
}

func TestReplay_All(t *testing.T) {
	g := testGraph(t)
	defer g.Close()
	g.StartSession("https://example.com")

	g.AddNode(Node{URL: "https://example.com", StatusCode: 200, DiscoveredAt: time.Now()})
	g.AddNode(Node{URL: "https://example.com/a", StatusCode: 200, DiscoveredAt: time.Now()})

	urls, err := GetReplayURLs(g, ReplayConfig{Strategy: ReplayAll}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 {
		t.Errorf("replay urls = %d, want 2", len(urls))
	}
}

func TestContentHash(t *testing.T) {
	h1 := ContentHash([]byte("hello"))
	h2 := ContentHash([]byte("hello"))
	h3 := ContentHash([]byte("world"))

	if h1 != h2 {
		t.Error("same content should have same hash")
	}
	if h1 == h3 {
		t.Error("different content should have different hash")
	}
}

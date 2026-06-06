// Package crawlgraph implements a directed graph of URLs discovered during
// crawling, with support for SQLite persistence, multiple export formats,
// and replay strategies for re-crawling changed or failed pages.
package crawlgraph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Node represents a single URL in the crawl graph.
type Node struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	StatusCode   int       `json:"status_code"`
	ContentType  string    `json:"content_type"`
	Depth        int       `json:"depth"`
	CrawlTimeMs  int64     `json:"crawl_time_ms"`
	ContentHash  string    `json:"content_hash"`
	ItemCount    int       `json:"item_count"`
	Error        string    `json:"error,omitempty"`
	ParentURL    string    `json:"parent_url,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// Edge represents a link between two nodes.
type Edge struct {
	FromURL  string `json:"from_url"`
	ToURL    string `json:"to_url"`
	LinkText string `json:"link_text"`
}

// CrawlGraph is a thread-safe directed graph with SQLite persistence.
type CrawlGraph struct {
	db     *sql.DB
	id     string // crawl session ID
	logger *slog.Logger
	mu     sync.RWMutex
}

// New creates or opens a crawl graph database.
func New(dbPath string, logger *slog.Logger) (*CrawlGraph, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS crawl_sessions (
			id         TEXT PRIMARY KEY,
			seed_url   TEXT NOT NULL,
			started_at TEXT NOT NULL,
			ended_at   TEXT,
			node_count INTEGER DEFAULT 0,
			edge_count INTEGER DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS nodes (
			id            TEXT PRIMARY KEY,
			session_id    TEXT NOT NULL,
			url           TEXT NOT NULL,
			status_code   INTEGER DEFAULT 0,
			content_type  TEXT,
			depth         INTEGER DEFAULT 0,
			crawl_time_ms INTEGER DEFAULT 0,
			content_hash  TEXT,
			item_count    INTEGER DEFAULT 0,
			error         TEXT,
			parent_url    TEXT,
			discovered_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_nodes_session ON nodes(session_id);
		CREATE INDEX IF NOT EXISTS idx_nodes_url ON nodes(url);
		CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status_code);

		CREATE TABLE IF NOT EXISTS edges (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			from_url   TEXT NOT NULL,
			to_url     TEXT NOT NULL,
			link_text  TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_edges_session ON edges(session_id);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}

	return &CrawlGraph{
		db:     db,
		id:     uuid.New().String(),
		logger: logger.With("component", "crawl_graph"),
	}, nil
}

// SessionID returns the current session ID.
func (g *CrawlGraph) SessionID() string { return g.id }

// StartSession records a new crawl session.
func (g *CrawlGraph) StartSession(seedURL string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	_, err := g.db.Exec(
		`INSERT INTO crawl_sessions (id, seed_url, started_at) VALUES (?, ?, ?)`,
		g.id, seedURL, time.Now().Format(time.RFC3339))
	return err
}

// AddNode adds or updates a node in the graph.
func (g *CrawlGraph) AddNode(node Node) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if node.ID == "" {
		node.ID = uuid.New().String()
	}

	_, err := g.db.Exec(
		`INSERT OR REPLACE INTO nodes
		 (id, session_id, url, status_code, content_type, depth, crawl_time_ms,
		  content_hash, item_count, error, parent_url, discovered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ID, g.id, node.URL, node.StatusCode, node.ContentType,
		node.Depth, node.CrawlTimeMs, node.ContentHash, node.ItemCount,
		node.Error, node.ParentURL, node.DiscoveredAt.Format(time.RFC3339))

	return err
}

// AddEdge adds a link between two URLs.
func (g *CrawlGraph) AddEdge(edge Edge) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	_, err := g.db.Exec(
		`INSERT INTO edges (session_id, from_url, to_url, link_text)
		 VALUES (?, ?, ?, ?)`,
		g.id, edge.FromURL, edge.ToURL, edge.LinkText)
	return err
}

// GetNodes retrieves all nodes for the current session.
func (g *CrawlGraph) GetNodes() ([]Node, error) {
	return g.getNodesByQuery(
		`SELECT id, url, status_code, content_type, depth, crawl_time_ms,
		        content_hash, item_count, error, parent_url, discovered_at
		 FROM nodes WHERE session_id = ? ORDER BY depth, discovered_at`, g.id)
}

// GetFailedNodes retrieves nodes with errors or non-2xx status.
func (g *CrawlGraph) GetFailedNodes() ([]Node, error) {
	return g.getNodesByQuery(
		`SELECT id, url, status_code, content_type, depth, crawl_time_ms,
		        content_hash, item_count, error, parent_url, discovered_at
		 FROM nodes WHERE session_id = ? AND (error != '' OR status_code >= 400)
		 ORDER BY depth`, g.id)
}

// GetNodesByDepth retrieves nodes at a specific depth.
func (g *CrawlGraph) GetNodesByDepth(depth int) ([]Node, error) {
	return g.getNodesByQuery(
		`SELECT id, url, status_code, content_type, depth, crawl_time_ms,
		        content_hash, item_count, error, parent_url, discovered_at
		 FROM nodes WHERE session_id = ? AND depth = ?`, g.id, depth)
}

// GetEdges retrieves all edges for the current session.
func (g *CrawlGraph) GetEdges() ([]Edge, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	rows, err := g.db.Query(
		`SELECT from_url, to_url, link_text FROM edges WHERE session_id = ?`, g.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []Edge
	for rows.Next() {
		var e Edge
		var linkText sql.NullString
		if err := rows.Scan(&e.FromURL, &e.ToURL, &linkText); err != nil {
			return nil, err
		}
		e.LinkText = linkText.String
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

// Stats returns summary statistics for the current session.
func (g *CrawlGraph) Stats() (GraphStats, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var stats GraphStats
	row := g.db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN error != '' OR status_code >= 400 THEN 1 ELSE 0 END), 0),
		        COALESCE(MAX(depth), 0),
		        COALESCE(SUM(crawl_time_ms), 0),
		        COALESCE(SUM(item_count), 0)
		 FROM nodes WHERE session_id = ?`, g.id)

	err := row.Scan(&stats.NodeCount, &stats.SuccessCount, &stats.ErrorCount,
		&stats.MaxDepth, &stats.TotalTimeMs, &stats.TotalItems)
	if err != nil {
		return stats, err
	}

	row = g.db.QueryRow(`SELECT COUNT(*) FROM edges WHERE session_id = ?`, g.id)
	_ = row.Scan(&stats.EdgeCount)

	return stats, nil
}

// Close closes the graph database.
func (g *CrawlGraph) Close() error { return g.db.Close() }

func (g *CrawlGraph) getNodesByQuery(query string, args ...any) ([]Node, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		var contentType, contentHash, errStr, parentURL sql.NullString
		var discoveredStr string

		err := rows.Scan(&n.ID, &n.URL, &n.StatusCode, &contentType, &n.Depth,
			&n.CrawlTimeMs, &contentHash, &n.ItemCount, &errStr, &parentURL, &discoveredStr)
		if err != nil {
			return nil, err
		}

		n.ContentType = contentType.String
		n.ContentHash = contentHash.String
		n.Error = errStr.String
		n.ParentURL = parentURL.String
		n.DiscoveredAt, _ = time.Parse(time.RFC3339, discoveredStr)

		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// GraphStats holds summary statistics for a crawl graph.
type GraphStats struct {
	NodeCount    int   `json:"node_count"`
	EdgeCount    int   `json:"edge_count"`
	SuccessCount int   `json:"success_count"`
	ErrorCount   int   `json:"error_count"`
	MaxDepth     int   `json:"max_depth"`
	TotalTimeMs  int64 `json:"total_time_ms"`
	TotalItems   int   `json:"total_items"`
}

// ExportJSON exports the graph as JSON.
func (g *CrawlGraph) ExportJSON() ([]byte, error) {
	nodes, err := g.GetNodes()
	if err != nil {
		return nil, err
	}
	edges, err := g.GetEdges()
	if err != nil {
		return nil, err
	}
	stats, _ := g.Stats()

	data := map[string]any{
		"session_id": g.id,
		"stats":      stats,
		"nodes":      nodes,
		"edges":      edges,
	}
	return json.MarshalIndent(data, "", "  ")
}

// ExportDOT exports the graph in GraphViz DOT format.
func (g *CrawlGraph) ExportDOT() (string, error) {
	nodes, err := g.GetNodes()
	if err != nil {
		return "", err
	}
	edges, err := g.GetEdges()
	if err != nil {
		return "", err
	}

	var sb fmt.Stringer = &dotBuilder{}
	b := sb.(*dotBuilder)
	b.writef("digraph crawl_graph {\n")
	b.writef("  rankdir=LR;\n")
	b.writef("  node [shape=box, style=filled, fillcolor=lightblue];\n\n")

	for _, n := range nodes {
		color := "lightblue"
		if n.StatusCode >= 400 || n.Error != "" {
			color = "lightcoral"
		} else if n.StatusCode >= 300 {
			color = "lightyellow"
		}
		label := truncateURL(n.URL, 40)
		b.writef("  \"%s\" [label=\"%s\\n[%d]\", fillcolor=%s];\n",
			n.URL, label, n.StatusCode, color)
	}
	b.writef("\n")
	for _, e := range edges {
		b.writef("  \"%s\" -> \"%s\";\n", e.FromURL, e.ToURL)
	}
	b.writef("}\n")

	return b.String(), nil
}

// ExportMermaid exports the graph in Mermaid format.
func (g *CrawlGraph) ExportMermaid() (string, error) {
	nodes, err := g.GetNodes()
	if err != nil {
		return "", err
	}
	edges, err := g.GetEdges()
	if err != nil {
		return "", err
	}

	var b dotBuilder
	b.writef("graph TD\n")

	nodeIDs := make(map[string]string)
	for i, n := range nodes {
		id := fmt.Sprintf("N%d", i)
		nodeIDs[n.URL] = id
		label := truncateURL(n.URL, 30)
		if n.Error != "" || n.StatusCode >= 400 {
			b.writef("  %s[\"%s ❌\"]\n", id, label)
		} else {
			b.writef("  %s[\"%s\"]\n", id, label)
		}
	}
	for _, e := range edges {
		from, ok1 := nodeIDs[e.FromURL]
		to, ok2 := nodeIDs[e.ToURL]
		if ok1 && ok2 {
			b.writef("  %s --> %s\n", from, to)
		}
	}

	return b.String(), nil
}

// ExportCSV exports nodes as CSV.
func (g *CrawlGraph) ExportCSV() (string, error) {
	nodes, err := g.GetNodes()
	if err != nil {
		return "", err
	}

	var b dotBuilder
	b.writef("url,status_code,content_type,depth,crawl_time_ms,item_count,error,parent_url\n")
	for _, n := range nodes {
		b.writef("%s,%d,%s,%d,%d,%d,%s,%s\n",
			n.URL, n.StatusCode, n.ContentType, n.Depth,
			n.CrawlTimeMs, n.ItemCount, n.Error, n.ParentURL)
	}
	return b.String(), nil
}

// --- Helpers ---

type dotBuilder struct {
	data []byte
}

func (b *dotBuilder) writef(format string, args ...any) {
	b.data = append(b.data, fmt.Sprintf(format, args...)...)
}

func (b *dotBuilder) String() string { return string(b.data) }

func truncateURL(u string, max int) string {
	if len(u) <= max {
		return u
	}
	return u[:max-3] + "..."
}

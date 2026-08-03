package crawlgraph

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
)

// ReplayStrategy determines which URLs to re-crawl.
type ReplayStrategy string

const (
	// ReplayErrors re-crawls nodes that had errors or non-2xx status codes.
	ReplayErrors ReplayStrategy = "errors"

	// ReplayChanged re-crawls nodes whose content hash differs from a reference session.
	ReplayChanged ReplayStrategy = "changed"

	// ReplayAll re-crawls all nodes from a previous session.
	ReplayAll ReplayStrategy = "all"

	// ReplayDepth re-crawls nodes at or below a specific depth.
	ReplayDepth ReplayStrategy = "depth"
)

// ReplayConfig configures a replay run.
type ReplayConfig struct {
	Strategy    ReplayStrategy
	ReferenceDB string // Path to reference crawl graph DB.
	MaxDepth    int    // For depth strategy.
	URLPattern  string // For pattern strategy.
}

// GetReplayURLs returns the list of URLs to re-crawl based on the strategy.
func GetReplayURLs(graph *CrawlGraph, cfg ReplayConfig, logger *slog.Logger) ([]string, error) {
	switch cfg.Strategy {
	case ReplayErrors:
		return replayErrors(graph)
	case ReplayChanged:
		return replayChanged(graph, cfg, logger)
	case ReplayAll:
		return replayAll(graph)
	case ReplayDepth:
		return replayDepth(graph, cfg.MaxDepth)
	default:
		return nil, fmt.Errorf("unknown replay strategy: %s", cfg.Strategy)
	}
}

func replayErrors(graph *CrawlGraph) ([]string, error) {
	nodes, err := graph.GetFailedNodes()
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(nodes))
	for _, n := range nodes {
		urls = append(urls, n.URL)
	}
	return urls, nil
}

func replayChanged(graph *CrawlGraph, cfg ReplayConfig, logger *slog.Logger) ([]string, error) {
	if cfg.ReferenceDB == "" {
		return nil, fmt.Errorf("reference DB required for changed strategy")
	}

	refGraph, err := New(cfg.ReferenceDB, logger)
	if err != nil {
		return nil, fmt.Errorf("open reference graph: %w", err)
	}
	defer refGraph.Close()

	currentNodes, err := graph.GetNodes()
	if err != nil {
		return nil, err
	}
	refNodes, err := refGraph.GetNodes()
	if err != nil {
		return nil, err
	}

	// Build hash map from reference.
	refHashes := make(map[string]string, len(refNodes))
	for _, n := range refNodes {
		refHashes[n.URL] = n.ContentHash
	}

	var changed []string
	for _, n := range currentNodes {
		refHash, exists := refHashes[n.URL]
		if !exists {
			// New page — include.
			changed = append(changed, n.URL)
		} else if n.ContentHash != refHash {
			// Content changed.
			changed = append(changed, n.URL)
		}
	}
	return changed, nil
}

func replayAll(graph *CrawlGraph) ([]string, error) {
	nodes, err := graph.GetNodes()
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(nodes))
	for _, n := range nodes {
		urls = append(urls, n.URL)
	}
	return urls, nil
}

func replayDepth(graph *CrawlGraph, maxDepth int) ([]string, error) {
	nodes, err := graph.GetNodes()
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, n := range nodes {
		if n.Depth <= maxDepth {
			urls = append(urls, n.URL)
		}
	}
	return urls, nil
}

// ContentHash computes a SHA256 hash of content for change detection.
func ContentHash(content []byte) string {
	h := sha256.Sum256(content)
	return fmt.Sprintf("%x", h)
}

package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MediaType classifies the type of media.
type MediaType string

const (
	MediaImage    MediaType = "image"
	MediaVideo    MediaType = "video"
	MediaAudio    MediaType = "audio"
	MediaDocument MediaType = "document"
	MediaOther    MediaType = "other"
)

// DownloadResult tracks a downloaded file.
type DownloadResult struct {
	URL         string        `json:"url"`
	LocalPath   string        `json:"local_path"`
	Filename    string        `json:"filename"`
	Size        int64         `json:"size"`
	ContentType string        `json:"content_type"`
	MediaType   MediaType     `json:"media_type"`
	Hash        string        `json:"hash"`
	Duration    time.Duration `json:"duration"`
}

// Downloader handles downloading and organizing media files.
type Downloader struct {
	outputDir  string
	client     *http.Client
	maxSize    int64
	concurrent int
	downloaded atomic.Int64
	logger     *slog.Logger
	mu         sync.Mutex
	seen       map[string]bool
}

// NewDownloader creates a new media downloader.
func NewDownloader(outputDir string, maxSizeMB int64, concurrent int, logger *slog.Logger) *Downloader {
	_ = os.MkdirAll(outputDir, 0o755)
	return &Downloader{
		outputDir:  outputDir,
		client:     &http.Client{Timeout: 60 * time.Second},
		maxSize:    maxSizeMB * 1024 * 1024,
		concurrent: concurrent,
		logger:     logger.With("component", "media_downloader"),
		seen:       make(map[string]bool),
	}
}

// Download downloads a file from the given URL.
func (d *Downloader) Download(ctx context.Context, rawURL string) (*DownloadResult, error) {
	d.mu.Lock()
	if d.seen[rawURL] {
		d.mu.Unlock()
		return nil, fmt.Errorf("already downloaded: %s", rawURL)
	}
	d.seen[rawURL] = true
	d.mu.Unlock()

	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: status %d", rawURL, resp.StatusCode)
	}

	// Check size
	if d.maxSize > 0 && resp.ContentLength > d.maxSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", resp.ContentLength, d.maxSize)
	}

	// Determine filename and type
	contentType := resp.Header.Get("Content-Type")
	mediaType := classifyMedia(contentType)
	filename := extractFilename(rawURL, contentType)

	// Create subdirectory by type
	subDir := filepath.Join(d.outputDir, string(mediaType))
	_ = os.MkdirAll(subDir, 0o755)
	localPath := filepath.Join(subDir, filename)

	// Write to file with hash computation
	f, err := os.Create(localPath)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)

	// Limited reader for safety
	reader := resp.Body
	if d.maxSize > 0 {
		reader = io.NopCloser(io.LimitReader(resp.Body, d.maxSize))
	}

	size, err := io.Copy(writer, reader)
	if err != nil {
		os.Remove(localPath)
		return nil, fmt.Errorf("write file: %w", err)
	}

	hash := hex.EncodeToString(hasher.Sum(nil))
	d.downloaded.Add(1)

	result := &DownloadResult{
		URL:         rawURL,
		LocalPath:   localPath,
		Filename:    filename,
		Size:        size,
		ContentType: contentType,
		MediaType:   mediaType,
		Hash:        hash,
		Duration:    time.Since(start),
	}

	d.logger.Debug("file downloaded",
		"url", rawURL,
		"size", size,
		"type", mediaType,
		"hash", hash[:16],
		"duration", result.Duration,
	)

	return result, nil
}

// DownloadBatch downloads multiple URLs concurrently.
func (d *Downloader) DownloadBatch(ctx context.Context, urls []string) []*DownloadResult {
	var results []*DownloadResult
	var mu sync.Mutex

	sem := make(chan struct{}, d.concurrent)
	var wg sync.WaitGroup

	for _, u := range urls {
		wg.Add(1)
		go func(rawURL string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := d.Download(ctx, rawURL)
			if err != nil {
				d.logger.Warn("download failed", "url", rawURL, "error", err)
				return
			}

			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(u)
	}

	wg.Wait()
	return results
}

// Stats returns download statistics.
func (d *Downloader) Stats() map[string]int64 {
	return map[string]int64{
		"total_downloaded": d.downloaded.Load(),
	}
}

// --- Perceptual Hashing ---

// PerceptualHash computes a simple perceptual hash for near-duplicate detection.
// This is a simplified implementation based on content hashing.
type PerceptualHasher struct {
	hashes map[string]string // hash -> URL
	mu     sync.RWMutex
}

// NewPerceptualHasher creates a new perceptual hasher.
func NewPerceptualHasher() *PerceptualHasher {
	return &PerceptualHasher{
		hashes: make(map[string]string),
	}
}

// IsDuplicate checks if a file is a near-duplicate of a previously seen file.
func (ph *PerceptualHasher) IsDuplicate(hash string) (bool, string) {
	ph.mu.RLock()
	defer ph.mu.RUnlock()
	if url, ok := ph.hashes[hash]; ok {
		return true, url
	}
	return false, ""
}

// Register stores a hash for future duplicate detection.
func (ph *PerceptualHasher) Register(hash, url string) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	ph.hashes[hash] = url
}

// --- File Metadata Extraction ---

// FileMetadata holds extracted metadata from a file.
type FileMetadata struct {
	Filename    string            `json:"filename"`
	Extension   string            `json:"extension"`
	Size        int64             `json:"size"`
	ContentType string            `json:"content_type"`
	MediaType   MediaType         `json:"media_type"`
	Properties  map[string]string `json:"properties,omitempty"`
}

// ExtractMetadata extracts metadata from a downloaded file.
func ExtractMetadata(localPath string) (*FileMetadata, error) {
	stat, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}

	ext := filepath.Ext(localPath)
	contentType := mime.TypeByExtension(ext)

	meta := &FileMetadata{
		Filename:    filepath.Base(localPath),
		Extension:   ext,
		Size:        stat.Size(),
		ContentType: contentType,
		MediaType:   classifyMedia(contentType),
		Properties:  make(map[string]string),
	}

	meta.Properties["modified"] = stat.ModTime().Format(time.RFC3339)
	meta.Properties["size_human"] = humanSize(stat.Size())

	return meta, nil
}

// --- Helpers ---

func classifyMedia(contentType string) MediaType {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "image/"):
		return MediaImage
	case strings.HasPrefix(ct, "video/"):
		return MediaVideo
	case strings.HasPrefix(ct, "audio/"):
		return MediaAudio
	case strings.HasPrefix(ct, "application/pdf"),
		strings.HasPrefix(ct, "application/msword"),
		strings.HasPrefix(ct, "application/vnd."):
		return MediaDocument
	default:
		return MediaOther
	}
}

func extractFilename(rawURL, contentType string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "unknown"
	}

	filename := path.Base(parsed.Path)
	if filename == "" || filename == "." || filename == "/" {
		hash := sha256.Sum256([]byte(rawURL))
		ext, _ := mime.ExtensionsByType(contentType)
		if len(ext) > 0 {
			filename = hex.EncodeToString(hash[:8]) + ext[0]
		} else {
			filename = hex.EncodeToString(hash[:8])
		}
	}

	return filename
}

func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

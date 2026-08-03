package storage

import (
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Storage is the interface for all storage backends.
type Storage interface {
	// Store persists a batch of items.
	Store(items []*types.Item) error

	// Close flushes pending writes and releases resources.
	Close() error

	// Name returns the storage backend identifier.
	Name() string
}

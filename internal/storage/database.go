package storage

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// MongoStorage writes items to a MongoDB collection.
type MongoStorage struct {
	client     *mongo.Client
	collection *mongo.Collection
	mu         sync.Mutex
	count      int
	logger     *slog.Logger
}

// NewMongoStorage creates a new MongoDB storage backend.
func NewMongoStorage(uri, database, collection string, logger *slog.Logger) (*MongoStorage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongodb connect: %w", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("mongodb ping: %w", err)
	}

	return &MongoStorage{
		client:     client,
		collection: client.Database(database).Collection(collection),
		logger:     logger.With("component", "mongo_storage"),
	}, nil
}

func (s *MongoStorage) Name() string { return "mongodb" }

func (s *MongoStorage) Store(items []*types.Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	docs := make([]any, len(items))
	for i, item := range items {
		doc := make(map[string]any, len(item.Fields)+3)
		doc["_source_url"] = item.URL
		doc["_timestamp"] = item.Timestamp
		doc["_spider"] = item.SpiderName
		for k, v := range item.Fields {
			doc[k] = v
		}
		docs[i] = doc
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := s.collection.InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("mongodb insert: %w", err)
	}

	s.count += len(items)
	s.logger.Debug("items stored in mongodb", "count", len(items), "total", s.count)
	return nil
}

func (s *MongoStorage) Close() error {
	s.logger.Info("mongodb storage closing", "total_items", s.count)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.client.Disconnect(ctx)
}

// --- Multi-Storage Fan-Out ---

// MultiStorage writes items to multiple backends simultaneously.
type MultiStorage struct {
	backends []Storage
	logger   *slog.Logger
}

// NewMultiStorage creates a storage that fans out to multiple backends.
func NewMultiStorage(backends []Storage, logger *slog.Logger) *MultiStorage {
	return &MultiStorage{
		backends: backends,
		logger:   logger.With("component", "multi_storage"),
	}
}

func (s *MultiStorage) Name() string { return "multi" }

func (s *MultiStorage) Store(items []*types.Item) error {
	var firstErr error
	for _, backend := range s.backends {
		if err := backend.Store(items); err != nil {
			s.logger.Error("backend store failed", "backend", backend.Name(), "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *MultiStorage) Close() error {
	var firstErr error
	for _, backend := range s.backends {
		if err := backend.Close(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

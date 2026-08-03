package types

import (
	"encoding/json"
	"time"
)

// Item represents a single scraped data record.
type Item struct {
	// Fields stores the extracted key-value data.
	Fields map[string]any

	// URL is the source page URL this item was extracted from.
	URL string

	// SpiderName identifies which spider produced this item.
	SpiderName string

	// Timestamp is when this item was created.
	Timestamp time.Time

	// Depth is the crawl depth at which this item was found.
	Depth int

	// Checksum is a hash of the item content for deduplication.
	Checksum string
}

// NewItem creates a new empty Item from a source URL.
func NewItem(sourceURL string) *Item {
	return &Item{
		Fields:    make(map[string]any),
		URL:       sourceURL,
		Timestamp: time.Now(),
	}
}

// Set sets a field value.
func (i *Item) Set(key string, value any) {
	i.Fields[key] = value
}

// Get retrieves a field value.
func (i *Item) Get(key string) (any, bool) {
	v, ok := i.Fields[key]
	return v, ok
}

// GetString retrieves a field value as a string.
func (i *Item) GetString(key string) string {
	v, ok := i.Fields[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// Has returns true if the field exists.
func (i *Item) Has(key string) bool {
	_, ok := i.Fields[key]
	return ok
}

// Delete removes a field.
func (i *Item) Delete(key string) {
	delete(i.Fields, key)
}

// Keys returns all field names.
func (i *Item) Keys() []string {
	keys := make([]string, 0, len(i.Fields))
	for k := range i.Fields {
		keys = append(keys, k)
	}
	return keys
}

// ToJSON serializes the item to JSON bytes.
func (i *Item) ToJSON() ([]byte, error) {
	return json.Marshal(struct {
		Fields     map[string]any `json:"fields"`
		URL        string         `json:"url"`
		SpiderName string         `json:"spider_name,omitempty"`
		Timestamp  time.Time      `json:"timestamp"`
		Depth      int            `json:"depth"`
	}{
		Fields:     i.Fields,
		URL:        i.URL,
		SpiderName: i.SpiderName,
		Timestamp:  i.Timestamp,
		Depth:      i.Depth,
	})
}

// ToFlatMap returns a flat map suitable for CSV export.
func (i *Item) ToFlatMap() map[string]string {
	flat := make(map[string]string, len(i.Fields)+3)
	flat["_url"] = i.URL
	flat["_spider"] = i.SpiderName
	flat["_timestamp"] = i.Timestamp.Format(time.RFC3339)

	for k, v := range i.Fields {
		switch val := v.(type) {
		case string:
			flat[k] = val
		case []byte:
			flat[k] = string(val)
		default:
			b, _ := json.Marshal(val)
			flat[k] = string(b)
		}
	}
	return flat
}

// Clone creates a deep copy of the item.
func (i *Item) Clone() *Item {
	clone := &Item{
		Fields:     make(map[string]any, len(i.Fields)),
		URL:        i.URL,
		SpiderName: i.SpiderName,
		Timestamp:  i.Timestamp,
		Depth:      i.Depth,
		Checksum:   i.Checksum,
	}
	for k, v := range i.Fields {
		clone.Fields[k] = v
	}
	return clone
}

package observability

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Tracer provides distributed tracing for request lifecycle tracking.
// Implements a lightweight tracing API compatible with OpenTelemetry concepts.
//
// Each crawl request creates a span: Fetch → Parse → Pipeline → Storage.
// Spans can be exported to Jaeger, Zipkin, or logged to stdout.
type Tracer struct {
	spans    []*Span
	mu       sync.RWMutex
	logger   *slog.Logger
	exporter SpanExporter
	enabled  bool
	nextID   atomic.Int64
}

// Span represents a unit of work in the request lifecycle.
type Span struct {
	TraceID   string         `json:"trace_id"`
	SpanID    string         `json:"span_id"`
	ParentID  string         `json:"parent_id,omitempty"`
	Operation string         `json:"operation"`
	Service   string         `json:"service"`
	StartTime time.Time      `json:"start_time"`
	EndTime   time.Time      `json:"end_time,omitempty"`
	Duration  time.Duration  `json:"duration,omitempty"`
	Status    SpanStatus     `json:"status"`
	Tags      map[string]any `json:"tags,omitempty"`
	Events    []SpanEvent    `json:"events,omitempty"`
	mu        sync.Mutex
}

// SpanStatus indicates the outcome of a span.
type SpanStatus int

const (
	SpanStatusUnset SpanStatus = iota
	SpanStatusOK
	SpanStatusError
)

// SpanEvent represents a notable event within a span.
type SpanEvent struct {
	Name      string         `json:"name"`
	Timestamp time.Time      `json:"timestamp"`
	Attrs     map[string]any `json:"attrs,omitempty"`
}

// SpanExporter exports completed spans to an external system.
type SpanExporter interface {
	Export(spans []*Span) error
	Close() error
}

// TracerConfig configures the tracer.
type TracerConfig struct {
	Enabled     bool
	ServiceName string
	SampleRate  float64
	MaxSpans    int
}

// DefaultTracerConfig returns sensible defaults.
func DefaultTracerConfig() *TracerConfig {
	return &TracerConfig{
		Enabled:     true,
		ServiceName: "scrapegoat",
		SampleRate:  1.0,
		MaxSpans:    10000,
	}
}

// NewTracer creates a new tracer.
func NewTracer(cfg *TracerConfig, logger *slog.Logger) *Tracer {
	if cfg == nil {
		cfg = DefaultTracerConfig()
	}
	return &Tracer{
		logger:  logger.With("component", "tracer"),
		enabled: cfg.Enabled,
	}
}

// SetExporter sets the span exporter.
func (t *Tracer) SetExporter(e SpanExporter) {
	t.exporter = e
}

// StartSpan begins a new span for the given operation.
func (t *Tracer) StartSpan(ctx context.Context, operation string) (*Span, context.Context) {
	if !t.enabled {
		return &Span{Operation: operation}, ctx
	}

	id := t.nextID.Add(1)
	span := &Span{
		TraceID:   traceIDFromContext(ctx, id),
		SpanID:    spanID(id),
		ParentID:  parentSpanFromContext(ctx),
		Operation: operation,
		Service:   "scrapegoat",
		StartTime: time.Now(),
		Status:    SpanStatusUnset,
		Tags:      make(map[string]any),
	}

	ctx = contextWithSpan(ctx, span)
	return span, ctx
}

// EndSpan completes a span and records it.
func (t *Tracer) EndSpan(span *Span) {
	if span == nil {
		return
	}

	span.mu.Lock()
	span.EndTime = time.Now()
	span.Duration = span.EndTime.Sub(span.StartTime)
	if span.Status == SpanStatusUnset {
		span.Status = SpanStatusOK
	}
	span.mu.Unlock()

	t.mu.Lock()
	t.spans = append(t.spans, span)
	t.mu.Unlock()
}

// AddTag adds a key-value tag to a span.
func AddTag(span *Span, key string, value any) {
	if span == nil {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	if span.Tags == nil {
		span.Tags = make(map[string]any)
	}
	span.Tags[key] = value
}

// AddEvent records an event within a span.
func AddEvent(span *Span, name string, attrs map[string]any) {
	if span == nil {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	span.Events = append(span.Events, SpanEvent{
		Name:      name,
		Timestamp: time.Now(),
		Attrs:     attrs,
	})
}

// SetError marks a span as failed.
func SetError(span *Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	span.Status = SpanStatusError
	if span.Tags == nil {
		span.Tags = make(map[string]any)
	}
	span.Tags["error"] = true
	span.Tags["error.message"] = err.Error()
}

// Flush exports all collected spans.
func (t *Tracer) Flush() error {
	if t.exporter == nil {
		return nil
	}

	t.mu.Lock()
	spans := t.spans
	t.spans = nil
	t.mu.Unlock()

	if len(spans) == 0 {
		return nil
	}

	return t.exporter.Export(spans)
}

// Stats returns tracing statistics.
func (t *Tracer) Stats() map[string]any {
	t.mu.RLock()
	defer t.mu.RUnlock()

	total := len(t.spans)
	errors := 0
	var totalDuration time.Duration

	for _, s := range t.spans {
		if s.Status == SpanStatusError {
			errors++
		}
		totalDuration += s.Duration
	}

	avgDuration := time.Duration(0)
	if total > 0 {
		avgDuration = totalDuration / time.Duration(total)
	}

	return map[string]any{
		"total_spans":     total,
		"error_spans":     errors,
		"avg_duration_ms": avgDuration.Milliseconds(),
		"enabled":         t.enabled,
	}
}

// Close flushes remaining spans and closes the exporter.
func (t *Tracer) Close() error {
	if err := t.Flush(); err != nil {
		return err
	}
	if t.exporter != nil {
		return t.exporter.Close()
	}
	return nil
}

// --- Context helpers ---

type spanContextKey struct{}

func contextWithSpan(ctx context.Context, span *Span) context.Context {
	return context.WithValue(ctx, spanContextKey{}, span)
}

func spanFromContext(ctx context.Context) *Span {
	span, _ := ctx.Value(spanContextKey{}).(*Span)
	return span
}

func parentSpanFromContext(ctx context.Context) string {
	if span := spanFromContext(ctx); span != nil {
		return span.SpanID
	}
	return ""
}

func traceIDFromContext(ctx context.Context, seed int64) string {
	if span := spanFromContext(ctx); span != nil {
		return span.TraceID
	}
	return spanID(seed)
}

func spanID(seed int64) string {
	return time.Now().Format("20060102150405") + "-" + itoa(seed)
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

// --- Built-in Exporters ---

// LogExporter exports spans to slog.
type LogExporter struct {
	logger *slog.Logger
}

// NewLogExporter creates a log-based span exporter.
func NewLogExporter(logger *slog.Logger) *LogExporter {
	return &LogExporter{logger: logger}
}

// Export logs each span.
func (e *LogExporter) Export(spans []*Span) error {
	for _, span := range spans {
		e.logger.Info("span",
			"trace_id", span.TraceID,
			"span_id", span.SpanID,
			"operation", span.Operation,
			"duration_ms", span.Duration.Milliseconds(),
			"status", span.Status,
			"tags", span.Tags,
		)
	}
	return nil
}

// Close is a no-op for log exporter.
func (e *LogExporter) Close() error {
	return nil
}

// Ensure exporters implement the interface.
var _ SpanExporter = (*LogExporter)(nil)

package transforms

import (
	"log/slog"
	"os"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newItem(fields map[string]any) *types.Item {
	return &types.Item{
		URL:    "https://example.com",
		Fields: fields,
	}
}

// --- Schema Validation Tests ---

func TestSchema_Validate_RequiredFields(t *testing.T) {
	schema := &Schema{
		Name: "test",
		Fields: []SchemaField{
			{Name: "title", Type: "string", Required: true},
			{Name: "price", Type: "number", Required: true},
		},
	}

	item := newItem(map[string]any{"title": "Widget"})
	result := schema.Validate(item)
	if result.Valid {
		t.Error("expected validation to fail for missing required field")
	}
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestSchema_Validate_TypeChecks(t *testing.T) {
	schema := &Schema{
		Name: "types",
		Fields: []SchemaField{
			{Name: "name", Type: "string"},
			{Name: "count", Type: "number"},
			{Name: "active", Type: "boolean"},
			{Name: "tags", Type: "array"},
			{Name: "url", Type: "url"},
			{Name: "email", Type: "email"},
		},
	}

	tests := []struct {
		name    string
		fields  map[string]any
		wantErr bool
	}{
		{"valid", map[string]any{"name": "x", "count": 42, "active": true, "tags": []any{"a"}, "url": "https://x.com", "email": "a@b.com"}, false},
		{"wrong string", map[string]any{"name": 123}, true},
		{"wrong number", map[string]any{"count": "abc"}, true},
		{"wrong boolean", map[string]any{"active": "yes"}, true},
		{"wrong array", map[string]any{"tags": "not-array"}, true},
		{"bad url", map[string]any{"url": "ftp://x"}, true},
		{"bad email", map[string]any{"email": "nope"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := newItem(tt.fields)
			result := schema.Validate(item)
			if result.Valid == tt.wantErr {
				t.Errorf("expected valid=%v, got %v (errors: %v)", !tt.wantErr, result.Valid, result.Errors)
			}
		})
	}
}

func TestSchema_Validate_StringConstraints(t *testing.T) {
	schema := &Schema{
		Name: "str",
		Fields: []SchemaField{
			{Name: "code", Type: "string", MinLen: 3, MaxLen: 10, Pattern: `^[A-Z]+$`},
		},
	}

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"valid", "ABC", false},
		{"too short", "AB", true},
		{"too long", "ABCDEFGHIJK", true},
		{"bad pattern", "abc", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := newItem(map[string]any{"code": tt.value})
			result := schema.Validate(item)
			if result.Valid == tt.wantErr {
				t.Errorf("expected valid=%v, got %v (errors: %v)", !tt.wantErr, result.Valid, result.Errors)
			}
		})
	}
}

func TestSchema_Validate_StrictMode(t *testing.T) {
	schema := &Schema{
		Name:   "strict",
		Strict: true,
		Fields: []SchemaField{
			{Name: "title", Type: "string"},
		},
	}

	item := newItem(map[string]any{"title": "ok", "unknown_field": "bad"})
	result := schema.Validate(item)
	if result.Valid {
		t.Error("strict mode should reject unknown fields")
	}
}

func TestSchema_Validate_OptionalFieldsOK(t *testing.T) {
	schema := &Schema{
		Name: "optional",
		Fields: []SchemaField{
			{Name: "title", Type: "string", Required: false},
			{Name: "desc", Type: "string", Required: false},
		},
	}

	item := newItem(map[string]any{})
	result := schema.Validate(item)
	if !result.Valid {
		t.Errorf("optional fields should not cause errors: %v", result.Errors)
	}
}

// --- Transform Function Tests ---

func TestTrimStrings(t *testing.T) {
	item := newItem(map[string]any{
		"title": "  hello world  ",
		"count": 42,
	})

	if err := TrimStrings()(item); err != nil {
		t.Fatal(err)
	}

	if item.Fields["title"] != "hello world" {
		t.Errorf("expected trimmed string, got %q", item.Fields["title"])
	}
	if item.Fields["count"] != 42 {
		t.Error("non-string fields should be unchanged")
	}
}

func TestRenameField(t *testing.T) {
	item := newItem(map[string]any{"old_name": "value"})
	if err := RenameField("old_name", "new_name")(item); err != nil {
		t.Fatal(err)
	}

	if _, exists := item.Fields["old_name"]; exists {
		t.Error("old field should be deleted")
	}
	if item.Fields["new_name"] != "value" {
		t.Errorf("expected 'value', got %v", item.Fields["new_name"])
	}
}

func TestRenameField_Missing(t *testing.T) {
	item := newItem(map[string]any{"other": "value"})
	if err := RenameField("missing", "new_name")(item); err != nil {
		t.Fatal(err)
	}
	// Should not create new field from missing source.
	if _, exists := item.Fields["new_name"]; exists {
		t.Error("should not create field from missing source")
	}
}

func TestSetDefault(t *testing.T) {
	item := newItem(map[string]any{})
	if err := SetDefault("status", "active")(item); err != nil {
		t.Fatal(err)
	}
	if item.Fields["status"] != "active" {
		t.Errorf("expected 'active', got %v", item.Fields["status"])
	}

	// Existing value should not be overwritten.
	item.Fields["status"] = "archived"
	if err := SetDefault("status", "active")(item); err != nil {
		t.Fatal(err)
	}
	if item.Fields["status"] != "archived" {
		t.Errorf("existing value should not be overwritten")
	}
}

func TestSetDefault_EmptyString(t *testing.T) {
	item := newItem(map[string]any{"status": ""})
	if err := SetDefault("status", "active")(item); err != nil {
		t.Fatal(err)
	}
	if item.Fields["status"] != "active" {
		t.Errorf("empty string should be treated as missing")
	}
}

func TestAddTimestamp(t *testing.T) {
	item := newItem(map[string]any{})
	if err := AddTimestamp("scraped_at")(item); err != nil {
		t.Fatal(err)
	}
	ts, ok := item.Fields["scraped_at"].(string)
	if !ok || ts == "" {
		t.Error("expected non-empty timestamp string")
	}
}

func TestDropFields(t *testing.T) {
	item := newItem(map[string]any{
		"keep":  "yes",
		"drop1": "no",
		"drop2": "no",
	})

	if err := DropFields("drop1", "drop2")(item); err != nil {
		t.Fatal(err)
	}

	if _, exists := item.Fields["drop1"]; exists {
		t.Error("drop1 should be removed")
	}
	if _, exists := item.Fields["drop2"]; exists {
		t.Error("drop2 should be removed")
	}
	if item.Fields["keep"] != "yes" {
		t.Error("keep should remain")
	}
}

func TestRegexReplace(t *testing.T) {
	item := newItem(map[string]any{"price": "$19.99 USD"})
	if err := RegexReplace("price", `[^0-9.]`, "")(item); err != nil {
		t.Fatal(err)
	}
	if item.Fields["price"] != "19.99" {
		t.Errorf("expected '19.99', got %q", item.Fields["price"])
	}
}

func TestComputeField(t *testing.T) {
	item := newItem(map[string]any{
		"first": "John",
		"last":  "Doe",
	})

	compute := ComputeField("full_name", func(fields map[string]any) any {
		return fields["first"].(string) + " " + fields["last"].(string)
	})

	if err := compute(item); err != nil {
		t.Fatal(err)
	}
	if item.Fields["full_name"] != "John Doe" {
		t.Errorf("expected 'John Doe', got %v", item.Fields["full_name"])
	}
}

// --- Pipeline Middleware Tests ---

func TestSchemaValidationMiddleware_Drop(t *testing.T) {
	schema := &Schema{
		Name:   "test",
		Fields: []SchemaField{{Name: "title", Type: "string", Required: true}},
	}

	mw := &SchemaValidationMiddleware{
		Schema: schema,
		Logger: testLogger(),
		OnFail: "drop",
	}

	// Valid item.
	item := newItem(map[string]any{"title": "ok"})
	result, err := mw.Process(item)
	if err != nil || result == nil {
		t.Error("valid item should pass through")
	}

	// Invalid item.
	item = newItem(map[string]any{})
	result, err = mw.Process(item)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("invalid item should be dropped (nil)")
	}
}

func TestSchemaValidationMiddleware_Annotate(t *testing.T) {
	schema := &Schema{
		Name:   "test",
		Fields: []SchemaField{{Name: "title", Type: "string", Required: true}},
	}

	mw := &SchemaValidationMiddleware{
		Schema: schema,
		Logger: testLogger(),
		OnFail: "annotate",
	}

	item := newItem(map[string]any{})
	result, err := mw.Process(item)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("annotate mode should not drop item")
	}
	if _, ok := result.Fields["_validation_errors"]; !ok {
		t.Error("annotate mode should add _validation_errors field")
	}
}

func TestSchemaValidationMiddleware_Log(t *testing.T) {
	schema := &Schema{
		Name:   "test",
		Fields: []SchemaField{{Name: "title", Type: "string", Required: true}},
	}

	mw := &SchemaValidationMiddleware{
		Schema: schema,
		Logger: testLogger(),
		OnFail: "log",
	}

	item := newItem(map[string]any{})
	result, err := mw.Process(item)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Error("log mode should not drop item")
	}
}

func TestTransformMiddleware_AppliesAll(t *testing.T) {
	mw := &TransformMiddleware{
		Transforms: []Transform{
			TrimStrings(),
			SetDefault("status", "active"),
			DropFields("_internal"),
		},
		Logger: testLogger(),
	}

	item := newItem(map[string]any{
		"title":     "  hello  ",
		"_internal": "secret",
	})

	result, err := mw.Process(item)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fields["title"] != "hello" {
		t.Errorf("trim not applied: %q", result.Fields["title"])
	}
	if result.Fields["status"] != "active" {
		t.Errorf("default not set: %v", result.Fields["status"])
	}
	if _, exists := result.Fields["_internal"]; exists {
		t.Error("drop not applied")
	}
}

func TestMiddleware_Name(t *testing.T) {
	sm := &SchemaValidationMiddleware{Schema: &Schema{}, Logger: testLogger(), OnFail: "log"}
	if sm.Name() != "schema_validation" {
		t.Errorf("unexpected name: %s", sm.Name())
	}

	tm := &TransformMiddleware{Logger: testLogger()}
	if tm.Name() != "transforms" {
		t.Errorf("unexpected name: %s", tm.Name())
	}
}

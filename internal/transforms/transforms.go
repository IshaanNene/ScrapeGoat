// Package transforms extends the pipeline with schema validation, data
// transformation, and enrichment middlewares.
package transforms

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// SchemaField defines validation rules for a single field.
type SchemaField struct {
	Name     string `json:"name" yaml:"name"`
	Type     string `json:"type" yaml:"type"` // string, number, boolean, array, url, email, date
	Required bool   `json:"required" yaml:"required"`
	Pattern  string `json:"pattern" yaml:"pattern"` // regex
	MinLen   int    `json:"min_len" yaml:"min_len"`
	MaxLen   int    `json:"max_len" yaml:"max_len"`
}

// Schema defines the expected shape and validation rules for items.
type Schema struct {
	Name   string        `json:"name" yaml:"name"`
	Fields []SchemaField `json:"fields" yaml:"fields"`
	Strict bool          `json:"strict" yaml:"strict"` // Reject unknown fields.
}

// ValidationResult contains the outcome of schema validation.
type ValidationResult struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

// Validate checks an item against the schema.
func (s *Schema) Validate(item *types.Item) ValidationResult {
	var errors []string

	for _, field := range s.Fields {
		val, exists := item.Fields[field.Name]
		if !exists || val == nil {
			if field.Required {
				errors = append(errors, fmt.Sprintf("missing required field: %s", field.Name))
			}
			continue
		}

		// Type validation.
		if err := validateType(field.Name, val, field.Type); err != "" {
			errors = append(errors, err)
		}

		// String length validation.
		if str, ok := val.(string); ok {
			if field.MinLen > 0 && len(str) < field.MinLen {
				errors = append(errors, fmt.Sprintf("%s: length %d < min %d", field.Name, len(str), field.MinLen))
			}
			if field.MaxLen > 0 && len(str) > field.MaxLen {
				errors = append(errors, fmt.Sprintf("%s: length %d > max %d", field.Name, len(str), field.MaxLen))
			}
			if field.Pattern != "" {
				re, err := regexp.Compile(field.Pattern)
				if err == nil && !re.MatchString(str) {
					errors = append(errors, fmt.Sprintf("%s: does not match pattern %s", field.Name, field.Pattern))
				}
			}
		}
	}

	// Strict mode: reject unknown fields.
	if s.Strict {
		fieldNames := make(map[string]bool, len(s.Fields))
		for _, f := range s.Fields {
			fieldNames[f.Name] = true
		}
		for key := range item.Fields {
			if !fieldNames[key] {
				errors = append(errors, fmt.Sprintf("unknown field: %s", key))
			}
		}
	}

	return ValidationResult{
		Valid:  len(errors) == 0,
		Errors: errors,
	}
}

func validateType(name string, val any, expectedType string) string {
	switch expectedType {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Sprintf("%s: expected string, got %T", name, val)
		}
	case "number":
		switch val.(type) {
		case int, int32, int64, float32, float64, json.Number:
			// OK.
		default:
			return fmt.Sprintf("%s: expected number, got %T", name, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Sprintf("%s: expected boolean, got %T", name, val)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Sprintf("%s: expected array, got %T", name, val)
		}
	case "url":
		str, ok := val.(string)
		if !ok {
			return fmt.Sprintf("%s: expected URL string, got %T", name, val)
		}
		if !strings.HasPrefix(str, "http://") && !strings.HasPrefix(str, "https://") {
			return fmt.Sprintf("%s: invalid URL: %s", name, str)
		}
	case "email":
		str, ok := val.(string)
		if !ok || !strings.Contains(str, "@") {
			return fmt.Sprintf("%s: invalid email", name)
		}
	}
	return ""
}

// --- Transform Functions ---

// Transform is a function that modifies an item in place.
type Transform func(item *types.Item) error

// TrimStrings trims whitespace from all string fields.
func TrimStrings() Transform {
	return func(item *types.Item) error {
		for key, val := range item.Fields {
			if str, ok := val.(string); ok {
				item.Fields[key] = strings.TrimSpace(str)
			}
		}
		return nil
	}
}

// RenameField renames a field.
func RenameField(from, to string) Transform {
	return func(item *types.Item) error {
		if val, ok := item.Fields[from]; ok {
			item.Fields[to] = val
			delete(item.Fields, from)
		}
		return nil
	}
}

// SetDefault sets a default value if the field is missing or empty.
func SetDefault(field string, defaultVal any) Transform {
	return func(item *types.Item) error {
		val, exists := item.Fields[field]
		if !exists || val == nil || val == "" {
			item.Fields[field] = defaultVal
		}
		return nil
	}
}

// AddTimestamp adds a timestamp field to items.
func AddTimestamp(field string) Transform {
	return func(item *types.Item) error {
		item.Fields[field] = time.Now().Format(time.RFC3339)
		return nil
	}
}

// DropFields removes specified fields from items.
func DropFields(fields ...string) Transform {
	return func(item *types.Item) error {
		for _, f := range fields {
			delete(item.Fields, f)
		}
		return nil
	}
}

// RegexReplace applies a regex replacement on a field.
func RegexReplace(field, pattern, replacement string) Transform {
	re := regexp.MustCompile(pattern)
	return func(item *types.Item) error {
		if str, ok := item.Fields[field].(string); ok {
			item.Fields[field] = re.ReplaceAllString(str, replacement)
		}
		return nil
	}
}

// ComputeField creates a new field by applying a function to existing fields.
func ComputeField(name string, compute func(fields map[string]any) any) Transform {
	return func(item *types.Item) error {
		item.Fields[name] = compute(item.Fields)
		return nil
	}
}

// --- Pipeline Middleware ---

// SchemaValidationMiddleware validates items against a schema and drops invalid ones.
type SchemaValidationMiddleware struct {
	Schema *Schema
	Logger *slog.Logger
	OnFail string // "drop", "log", "annotate"
}

// Process validates the item.
func (m *SchemaValidationMiddleware) Process(item *types.Item) (*types.Item, error) {
	result := m.Schema.Validate(item)
	if result.Valid {
		return item, nil
	}

	switch m.OnFail {
	case "drop":
		m.Logger.Warn("item dropped by schema validation", "errors", result.Errors, "url", item.URL)
		return nil, nil
	case "annotate":
		item.Fields["_validation_errors"] = result.Errors
		return item, nil
	default: // "log"
		m.Logger.Warn("schema validation failed", "errors", result.Errors, "url", item.URL)
		return item, nil
	}
}

// Name returns the middleware name.
func (m *SchemaValidationMiddleware) Name() string { return "schema_validation" }

// TransformMiddleware applies a sequence of transforms to items.
type TransformMiddleware struct {
	Transforms []Transform
	Logger     *slog.Logger
}

// Process applies all transforms in order.
func (m *TransformMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, t := range m.Transforms {
		if err := t(item); err != nil {
			m.Logger.Warn("transform error", "error", err)
			return item, nil
		}
	}
	return item, nil
}

// Name returns the middleware name.
func (m *TransformMiddleware) Name() string { return "transforms" }

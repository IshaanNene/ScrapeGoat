package pipeline

import (
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// --- Advanced Middleware ---

// HTMLSanitizeMiddleware strips HTML tags from string fields.
type HTMLSanitizeMiddleware struct {
	stripRe *regexp.Regexp
}

func NewHTMLSanitizeMiddleware() *HTMLSanitizeMiddleware {
	return &HTMLSanitizeMiddleware{
		stripRe: regexp.MustCompile(`<[^>]*>`),
	}
}

func (m *HTMLSanitizeMiddleware) Name() string { return "html_sanitize" }

func (m *HTMLSanitizeMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, key := range item.Keys() {
		if s := item.GetString(key); s != "" {
			// Strip HTML tags
			cleaned := m.stripRe.ReplaceAllString(s, "")
			// Decode HTML entities
			cleaned = html.UnescapeString(cleaned)
			// Normalize whitespace
			cleaned = strings.Join(strings.Fields(cleaned), " ")
			item.Set(key, cleaned)
		}
	}
	return item, nil
}

// DateNormalizeMiddleware normalizes date fields to a standard format.
type DateNormalizeMiddleware struct {
	fields    []string
	outFormat string
	inFormats []string
}

func NewDateNormalizeMiddleware(fields []string, outFormat string) *DateNormalizeMiddleware {
	if outFormat == "" {
		outFormat = time.RFC3339
	}
	return &DateNormalizeMiddleware{
		fields:    fields,
		outFormat: outFormat,
		inFormats: []string{
			time.RFC3339,
			time.RFC1123,
			time.RFC1123Z,
			time.RFC822,
			time.RFC822Z,
			"2006-01-02",
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
			"01/02/2006",
			"02/01/2006",
			"January 2, 2006",
			"Jan 2, 2006",
			"2 January 2006",
			"2 Jan 2006",
			"Mon, 02 Jan 2006",
			"02-Jan-2006",
			"2006/01/02",
			"01-02-2006",
			"Mon Jan 2 15:04:05 2006",
		},
	}
}

func (m *DateNormalizeMiddleware) Name() string { return "date_normalize" }

func (m *DateNormalizeMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, field := range m.fields {
		s := item.GetString(field)
		if s == "" {
			continue
		}
		s = strings.TrimSpace(s)

		for _, format := range m.inFormats {
			t, err := time.Parse(format, s)
			if err == nil {
				item.Set(field, t.Format(m.outFormat))
				break
			}
		}
	}
	return item, nil
}

// CurrencyNormalizeMiddleware normalizes currency values to numeric.
type CurrencyNormalizeMiddleware struct {
	fields  []string
	stripRe *regexp.Regexp
}

func NewCurrencyNormalizeMiddleware(fields []string) *CurrencyNormalizeMiddleware {
	return &CurrencyNormalizeMiddleware{
		fields:  fields,
		stripRe: regexp.MustCompile(`[^0-9.,\-]`),
	}
}

func (m *CurrencyNormalizeMiddleware) Name() string { return "currency_normalize" }

func (m *CurrencyNormalizeMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, field := range m.fields {
		s := item.GetString(field)
		if s == "" {
			continue
		}

		// Extract numeric value
		numeric := m.stripRe.ReplaceAllString(s, "")

		// Handle European format (1.234,56 -> 1234.56)
		if strings.Contains(numeric, ",") {
			lastComma := strings.LastIndex(numeric, ",")
			lastDot := strings.LastIndex(numeric, ".")
			if lastComma > lastDot {
				// European: 1.234,56
				numeric = strings.ReplaceAll(numeric, ".", "")
				numeric = strings.Replace(numeric, ",", ".", 1)
			} else {
				// US: 1,234.56
				numeric = strings.ReplaceAll(numeric, ",", "")
			}
		}

		item.Set(field, numeric)
	}
	return item, nil
}

// TypeCoercionMiddleware converts field values to target types.
type TypeCoercionMiddleware struct {
	coercions map[string]string // field -> "int", "float", "bool", "string"
}

func NewTypeCoercionMiddleware(coercions map[string]string) *TypeCoercionMiddleware {
	return &TypeCoercionMiddleware{coercions: coercions}
}

func (m *TypeCoercionMiddleware) Name() string { return "type_coercion" }

func (m *TypeCoercionMiddleware) Process(item *types.Item) (*types.Item, error) {
	for field, targetType := range m.coercions {
		val, ok := item.Get(field)
		if !ok {
			continue
		}

		s := fmt.Sprintf("%v", val)

		switch targetType {
		case "int":
			if i, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				item.Set(field, i)
			}
		case "float":
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				item.Set(field, f)
			}
		case "bool":
			lower := strings.ToLower(s)
			item.Set(field, lower == "true" || lower == "1" || lower == "yes")
		case "string":
			item.Set(field, s)
		}
	}
	return item, nil
}

// PIIRedactMiddleware detects and redacts personally identifiable information.
type PIIRedactMiddleware struct {
	patterns map[string]*regexp.Regexp
	logger   *slog.Logger
}

func NewPIIRedactMiddleware(logger *slog.Logger) *PIIRedactMiddleware {
	return &PIIRedactMiddleware{
		patterns: map[string]*regexp.Regexp{
			"email":       regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
			"phone_us":    regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`),
			"phone_intl":  regexp.MustCompile(`\+\d{1,3}[-.\s]?\(?\d{1,4}\)?[-.\s]?\d{1,4}[-.\s]?\d{1,9}`),
			"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			"credit_card": regexp.MustCompile(`\b(?:\d{4}[-\s]?){3}\d{4}\b`),
			"ip_v4":       regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`),
		},
		logger: logger.With("component", "pii_redact"),
	}
}

func (m *PIIRedactMiddleware) Name() string { return "pii_redact" }

func (m *PIIRedactMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, key := range item.Keys() {
		s := item.GetString(key)
		if s == "" {
			continue
		}

		for piiType, re := range m.patterns {
			if re.MatchString(s) {
				s = re.ReplaceAllString(s, "[REDACTED_"+strings.ToUpper(piiType)+"]")
				m.logger.Debug("PII redacted", "field", key, "type", piiType)
			}
		}

		item.Set(key, s)
	}
	return item, nil
}

// FieldValidateMiddleware validates field values with regex patterns.
type FieldValidateMiddleware struct {
	validations map[string]*regexp.Regexp // field -> validation pattern
	dropInvalid bool
}

func NewFieldValidateMiddleware(patterns map[string]string, dropInvalid bool) (*FieldValidateMiddleware, error) {
	compiled := make(map[string]*regexp.Regexp, len(patterns))
	for field, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid validation regex for %q: %w", field, err)
		}
		compiled[field] = re
	}
	return &FieldValidateMiddleware{
		validations: compiled,
		dropInvalid: dropInvalid,
	}, nil
}

func (m *FieldValidateMiddleware) Name() string { return "field_validate" }

func (m *FieldValidateMiddleware) Process(item *types.Item) (*types.Item, error) {
	for field, re := range m.validations {
		s := item.GetString(field)
		if s == "" {
			continue
		}
		if !re.MatchString(s) {
			if m.dropInvalid {
				return nil, nil // Drop item
			}
			item.Delete(field) // Remove invalid field
		}
	}
	return item, nil
}

// WordCountMiddleware adds a word count field for specified text fields.
type WordCountMiddleware struct {
	fields []string
	suffix string
}

func NewWordCountMiddleware(fields []string) *WordCountMiddleware {
	return &WordCountMiddleware{
		fields: fields,
		suffix: "_word_count",
	}
}

func (m *WordCountMiddleware) Name() string { return "word_count" }

func (m *WordCountMiddleware) Process(item *types.Item) (*types.Item, error) {
	for _, field := range m.fields {
		s := item.GetString(field)
		if s == "" {
			continue
		}
		words := strings.Fields(s)
		item.Set(field+m.suffix, len(words))
	}
	return item, nil
}

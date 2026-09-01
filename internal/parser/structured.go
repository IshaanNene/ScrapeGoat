package parser

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// StructuredDataType identifies the type of structured data.
type StructuredDataType string

const (
	JSONLD      StructuredDataType = "json-ld"
	Microdata   StructuredDataType = "microdata"
	OpenGraph   StructuredDataType = "opengraph"
	TwitterCard StructuredDataType = "twitter_card"
	RDFa        StructuredDataType = "rdfa"
	MetaTags    StructuredDataType = "meta"
)

// StructuredData represents extracted structured data from a page.
type StructuredData struct {
	Type StructuredDataType `json:"type"`
	Data map[string]any     `json:"data"`
	Raw  string             `json:"raw,omitempty"`
}

// StructuredDataExtractor extracts JSON-LD, Microdata, OpenGraph, etc.
type StructuredDataExtractor struct {
	logger *slog.Logger
}

// NewStructuredDataExtractor creates a new structured data extractor.
func NewStructuredDataExtractor(logger *slog.Logger) *StructuredDataExtractor {
	return &StructuredDataExtractor{
		logger: logger.With("component", "structured_data"),
	}
}

// Extract finds and parses all structured data in a response.
func (sde *StructuredDataExtractor) Extract(resp *types.Response) ([]StructuredData, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var results []StructuredData

	// JSON-LD
	jsonLDResults := sde.extractJSONLD(doc)
	results = append(results, jsonLDResults...)

	// OpenGraph
	ogData := sde.extractOpenGraph(doc)
	if len(ogData.Data) > 0 {
		results = append(results, ogData)
	}

	// Twitter Cards
	tcData := sde.extractTwitterCard(doc)
	if len(tcData.Data) > 0 {
		results = append(results, tcData)
	}

	// Microdata
	mdResults := sde.extractMicrodata(doc)
	results = append(results, mdResults...)

	// Standard meta tags
	metaData := sde.extractMetaTags(doc)
	if len(metaData.Data) > 0 {
		results = append(results, metaData)
	}

	return results, nil
}

// extractJSONLD parses <script type="application/ld+json"> elements.
func (sde *StructuredDataExtractor) extractJSONLD(doc *goquery.Document) []StructuredData {
	var results []StructuredData

	doc.Find(`script[type="application/ld+json"]`).Each(func(i int, sel *goquery.Selection) {
		raw := strings.TrimSpace(sel.Text())
		if raw == "" {
			return
		}

		// Try parsing as single object
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err == nil {
			results = append(results, StructuredData{
				Type: JSONLD,
				Data: data,
				Raw:  raw,
			})
			return
		}

		// Try parsing as array
		var dataArr []map[string]any
		if err := json.Unmarshal([]byte(raw), &dataArr); err == nil {
			for _, d := range dataArr {
				results = append(results, StructuredData{
					Type: JSONLD,
					Data: d,
					Raw:  raw,
				})
			}
		}
	})

	return results
}

// extractOpenGraph parses og: meta tags.
func (sde *StructuredDataExtractor) extractOpenGraph(doc *goquery.Document) StructuredData {
	data := make(map[string]any)

	doc.Find(`meta[property^="og:"]`).Each(func(i int, sel *goquery.Selection) {
		property, _ := sel.Attr("property")
		content, _ := sel.Attr("content")
		if property != "" && content != "" {
			key := strings.TrimPrefix(property, "og:")
			data[key] = content
		}
	})

	return StructuredData{Type: OpenGraph, Data: data}
}

// extractTwitterCard parses twitter: meta tags.
func (sde *StructuredDataExtractor) extractTwitterCard(doc *goquery.Document) StructuredData {
	data := make(map[string]any)

	doc.Find(`meta[name^="twitter:"], meta[property^="twitter:"]`).Each(func(i int, sel *goquery.Selection) {
		name, _ := sel.Attr("name")
		if name == "" {
			name, _ = sel.Attr("property")
		}
		content, _ := sel.Attr("content")
		if name != "" && content != "" {
			key := strings.TrimPrefix(name, "twitter:")
			data[key] = content
		}
	})

	return StructuredData{Type: TwitterCard, Data: data}
}

// extractMicrodata parses elements with itemscope/itemprop attributes.
func (sde *StructuredDataExtractor) extractMicrodata(doc *goquery.Document) []StructuredData {
	var results []StructuredData

	// Find top-level itemscope elements
	doc.Find("[itemscope]:not([itemscope] [itemscope])").Each(func(i int, sel *goquery.Selection) {
		data := make(map[string]any)

		itemType, _ := sel.Attr("itemtype")
		if itemType != "" {
			data["@type"] = itemType
		}

		sel.Find("[itemprop]").Each(func(j int, prop *goquery.Selection) {
			name, _ := prop.Attr("itemprop")
			if name == "" {
				return
			}

			var value string
			if href, exists := prop.Attr("href"); exists {
				value = href
			} else if src, exists := prop.Attr("src"); exists {
				value = src
			} else if content, exists := prop.Attr("content"); exists {
				value = content
			} else if datetime, exists := prop.Attr("datetime"); exists {
				value = datetime
			} else {
				value = strings.TrimSpace(prop.Text())
			}

			if value != "" {
				data[name] = value
			}
		})

		if len(data) > 0 {
			results = append(results, StructuredData{
				Type: Microdata,
				Data: data,
			})
		}
	})

	return results
}

// extractMetaTags parses standard meta tags (description, keywords, author, etc.).
func (sde *StructuredDataExtractor) extractMetaTags(doc *goquery.Document) StructuredData {
	data := make(map[string]any)

	// Title
	title := strings.TrimSpace(doc.Find("title").First().Text())
	if title != "" {
		data["title"] = title
	}

	// Standard meta tags
	metaNames := []string{
		"description", "keywords", "author", "robots",
		"viewport", "generator", "theme-color",
		"application-name", "msapplication-TileColor",
	}

	for _, name := range metaNames {
		content, exists := doc.Find(
			`meta[name="` + name + `"]`,
		).Attr("content")
		if exists && content != "" {
			data[name] = content
		}
	}

	// Canonical URL
	canonical, exists := doc.Find(`link[rel="canonical"]`).Attr("href")
	if exists && canonical != "" {
		data["canonical"] = canonical
	}

	// Favicon
	favicon, exists := doc.Find(`link[rel="icon"], link[rel="shortcut icon"]`).Attr("href")
	if exists && favicon != "" {
		data["favicon"] = favicon
	}

	// Alternate language links
	var hreflangs []map[string]string
	doc.Find(`link[rel="alternate"][hreflang]`).Each(func(i int, sel *goquery.Selection) {
		lang, _ := sel.Attr("hreflang")
		href, _ := sel.Attr("href")
		if lang != "" && href != "" {
			hreflangs = append(hreflangs, map[string]string{
				"lang": lang,
				"href": href,
			})
		}
	})
	if len(hreflangs) > 0 {
		data["hreflang"] = hreflangs
	}

	return StructuredData{Type: MetaTags, Data: data}
}

// StructuredDataToItem converts structured data results into a types.Item, as a
// view over StructuredDataToAssertions.
func StructuredDataToItem(results []StructuredData, sourceURL string) *types.Item {
	return ItemFromAssertions(sourceURL, StructuredDataToAssertions(results))
}

// StructuredDataToAssertions records what the page declared about itself.
//
// One assertion per field, not per occurrence. A page carrying two JSON-LD blocks
// yielded one json_ld field with the last block's data, and that is preserved
// rather than turned into a list: the collapse happens here, where the derivation
// knows it is choosing, instead of in the projection, where it would look like an
// accident of how many blocks the page happened to have.
func StructuredDataToAssertions(results []StructuredData) []provenance.Assertion {
	if len(results) == 0 {
		return nil
	}

	byField := map[string]provenance.Assertion{}
	set := func(field, method string, value any) {
		byField[field] = singleAssertion(field, method, structuredVersion, value)
	}

	for _, sd := range results {
		switch sd.Type {
		case JSONLD:
			set("json_ld", "structured:jsonld", sd.Data)
		case OpenGraph:
			set("opengraph", "structured:opengraph", sd.Data)
		case TwitterCard:
			set("twitter_card", "structured:twitter_card", sd.Data)
		case Microdata:
			set("microdata", "structured:microdata", sd.Data)
		case MetaTags:
			for k, v := range sd.Data {
				set("meta_"+k, "structured:meta@"+k, v)
			}
		}
	}

	// Sorted by field so the corpus rows come out in the same order every run.
	// The map above is keyed for last-wins, not for iteration.
	fields := make([]string, 0, len(byField))
	for f := range byField {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	out := make([]provenance.Assertion, 0, len(fields))
	for _, f := range fields {
		out = append(out, byField[f])
	}
	return out
}

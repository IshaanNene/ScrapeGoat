package parser

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// AutoExtractor automatically extracts structured data from any webpage
// without requiring manual selectors. It combines multiple extraction
// strategies: JSON-LD, OpenGraph, meta tags, table data, product patterns,
// and heuristic CSS detection.
//
// This is the "viral feature" — run `scrapegoat extract https://site.com/products`
// and get clean JSON output automatically.
type AutoExtractor struct {
	logger *slog.Logger
}

// NewAutoExtractor creates a new auto-extractor.
func NewAutoExtractor(logger *slog.Logger) *AutoExtractor {
	return &AutoExtractor{
		logger: logger.With("component", "auto_extractor"),
	}
}

// ExtractedData holds the auto-extracted structured data from a page.
type ExtractedData struct {
	URL         string            `json:"url"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type"` // product, article, listing, generic
	Data        []map[string]any  `json:"data"`
	Meta        map[string]string `json:"meta,omitempty"`
	Links       []ExtractedLink   `json:"links,omitempty"`
	Images      []ExtractedImage  `json:"images,omitempty"`
	Tables      []ExtractedTable  `json:"tables,omitempty"`
	JSONLD      []map[string]any  `json:"json_ld,omitempty"`
}

// ExtractedLink represents a discovered link.
type ExtractedLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
	Rel  string `json:"rel,omitempty"`
}

// ExtractedImage represents a discovered image.
type ExtractedImage struct {
	URL string `json:"url"`
	Alt string `json:"alt,omitempty"`
}

// ExtractedTable represents a table found on the page.
type ExtractedTable struct {
	Headers []string            `json:"headers"`
	Rows    []map[string]string `json:"rows"`
}

// Extract performs automatic data extraction on a response.
func (ae *AutoExtractor) Extract(resp *types.Response) (*ExtractedData, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, fmt.Errorf("parse document: %w", err)
	}

	result := &ExtractedData{
		URL:  resp.Request.URLString(),
		Meta: make(map[string]string),
		Data: make([]map[string]any, 0),
	}

	// 1. Extract basic meta info
	ae.extractMeta(doc, result)

	// 2. Extract JSON-LD structured data
	ae.extractJSONLD(doc, result)

	// 3. Extract OpenGraph data
	ae.extractOpenGraph(doc, result)

	// 4. Extract tables
	ae.extractTables(doc, result)

	// 5. Extract product data (price, name, image patterns)
	ae.extractProducts(doc, result)

	// 6. Extract article/content data
	ae.extractArticles(doc, result)

	// 7. Extract links and images
	ae.extractLinks(doc, result)
	ae.extractImages(doc, result)

	// 8. Determine page type
	ae.classifyPage(result)

	return result, nil
}

// extractMeta extracts basic meta information.
func (ae *AutoExtractor) extractMeta(doc *goquery.Document, result *ExtractedData) {
	result.Title = strings.TrimSpace(doc.Find("title").Text())

	doc.Find("meta").Each(func(i int, s *goquery.Selection) {
		if name, _ := s.Attr("name"); name != "" {
			if content, _ := s.Attr("content"); content != "" {
				result.Meta[name] = content
				if name == "description" {
					result.Description = content
				}
			}
		}
	})
}

// extractJSONLD extracts JSON-LD structured data.
func (ae *AutoExtractor) extractJSONLD(doc *goquery.Document, result *ExtractedData) {
	doc.Find(`script[type="application/ld+json"]`).Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text == "" {
			return
		}

		var data any
		if err := json.Unmarshal([]byte(text), &data); err != nil {
			return
		}

		switch v := data.(type) {
		case map[string]any:
			result.JSONLD = append(result.JSONLD, v)
			result.Data = append(result.Data, v)
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					result.JSONLD = append(result.JSONLD, m)
					result.Data = append(result.Data, m)
				}
			}
		}
	})
}

// extractOpenGraph extracts OpenGraph / Twitter Card metadata.
func (ae *AutoExtractor) extractOpenGraph(doc *goquery.Document, result *ExtractedData) {
	ogData := make(map[string]any)

	doc.Find(`meta[property^="og:"]`).Each(func(i int, s *goquery.Selection) {
		prop, _ := s.Attr("property")
		content, _ := s.Attr("content")
		key := strings.TrimPrefix(prop, "og:")
		ogData[key] = content
	})

	doc.Find(`meta[name^="twitter:"]`).Each(func(i int, s *goquery.Selection) {
		name, _ := s.Attr("name")
		content, _ := s.Attr("content")
		key := "twitter_" + strings.TrimPrefix(name, "twitter:")
		ogData[key] = content
	})

	if len(ogData) > 0 {
		result.Data = append(result.Data, ogData)
	}
}

// extractTables extracts HTML tables as structured data.
func (ae *AutoExtractor) extractTables(doc *goquery.Document, result *ExtractedData) {
	doc.Find("table").Each(func(i int, table *goquery.Selection) {
		var headers []string
		table.Find("thead th, thead td, tr:first-child th").Each(func(j int, th *goquery.Selection) {
			headers = append(headers, strings.TrimSpace(th.Text()))
		})

		// If no thead, try first row
		if len(headers) == 0 {
			table.Find("tr:first-child td").Each(func(j int, td *goquery.Selection) {
				text := strings.TrimSpace(td.Text())
				if text != "" {
					headers = append(headers, text)
				}
			})
		}

		if len(headers) == 0 {
			return
		}

		var rows []map[string]string
		startRow := 0
		if table.Find("thead").Length() == 0 {
			startRow = 1
		}

		table.Find("tbody tr, tr").Each(func(j int, tr *goquery.Selection) {
			if j < startRow {
				return
			}
			row := make(map[string]string)
			tr.Find("td").Each(func(k int, td *goquery.Selection) {
				if k < len(headers) {
					row[headers[k]] = strings.TrimSpace(td.Text())
				}
			})
			if len(row) > 0 {
				rows = append(rows, row)
			}
		})

		if len(rows) > 0 {
			result.Tables = append(result.Tables, ExtractedTable{
				Headers: headers,
				Rows:    rows,
			})
		}
	})
}

// Product patterns for auto-detection
var (
	pricePattern = regexp.MustCompile(`[\$€£¥₹]\s*[\d,]+\.?\d*|\d+[\.,]\d{2}\s*(?:USD|EUR|GBP|JPY|INR)`)
)

// extractProducts attempts to extract product information using common patterns.
func (ae *AutoExtractor) extractProducts(doc *goquery.Document, result *ExtractedData) {
	// Common product selectors
	productSelectors := []string{
		"[itemtype*='Product']",
		".product",
		".product-card",
		".product-item",
		".product-grid-item",
		"[data-product]",
		".item-card",
	}

	for _, selector := range productSelectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			product := make(map[string]any)
			product["_type"] = "product"

			// Try to extract name
			nameSelectors := []string{"h1", "h2", "h3", ".product-title", ".product-name", "[itemprop='name']", ".title", "a"}
			for _, ns := range nameSelectors {
				name := strings.TrimSpace(s.Find(ns).First().Text())
				if name != "" && len(name) < 200 {
					product["name"] = name
					break
				}
			}

			// Try to extract price
			priceSelectors := []string{".price", "[itemprop='price']", ".product-price", ".cost", ".amount"}
			for _, ps := range priceSelectors {
				price := strings.TrimSpace(s.Find(ps).First().Text())
				if price != "" {
					product["price"] = price
					break
				}
			}
			// Fallback: regex price from full text
			if _, ok := product["price"]; !ok {
				text := s.Text()
				if match := pricePattern.FindString(text); match != "" {
					product["price"] = match
				}
			}

			// Try to extract image
			if img := s.Find("img").First(); img.Length() > 0 {
				if src, ok := img.Attr("src"); ok {
					product["image"] = src
				}
				if alt, ok := img.Attr("alt"); ok {
					product["image_alt"] = alt
				}
			}

			// Try to extract link
			if a := s.Find("a").First(); a.Length() > 0 {
				if href, ok := a.Attr("href"); ok {
					product["url"] = href
				}
			}

			// Try to extract rating
			ratingSelectors := []string{".rating", "[itemprop='ratingValue']", ".stars", ".review-score"}
			for _, rs := range ratingSelectors {
				rating := strings.TrimSpace(s.Find(rs).First().Text())
				if rating != "" {
					product["rating"] = rating
					break
				}
			}

			// Try to extract description
			descSelectors := []string{".description", "[itemprop='description']", ".product-desc", "p"}
			for _, ds := range descSelectors {
				desc := strings.TrimSpace(s.Find(ds).First().Text())
				if desc != "" && len(desc) > 10 && len(desc) < 1000 {
					product["description"] = desc
					break
				}
			}

			if len(product) > 1 { // More than just _type
				result.Data = append(result.Data, product)
			}
		})
	}
}

// extractArticles attempts to extract article/content data.
func (ae *AutoExtractor) extractArticles(doc *goquery.Document, result *ExtractedData) {
	articleSelectors := []string{
		"article",
		"[itemtype*='Article']",
		"[itemtype*='NewsArticle']",
		"[itemtype*='BlogPosting']",
		".post",
		".article",
		".blog-post",
		".entry-content",
	}

	for _, selector := range articleSelectors {
		doc.Find(selector).Each(func(i int, s *goquery.Selection) {
			article := make(map[string]any)
			article["_type"] = "article"

			// Title
			if title := strings.TrimSpace(s.Find("h1, h2, .title, .headline").First().Text()); title != "" {
				article["title"] = title
			}

			// Author
			authorSelectors := []string{".author", "[itemprop='author']", "[rel='author']", ".byline"}
			for _, as := range authorSelectors {
				author := strings.TrimSpace(s.Find(as).First().Text())
				if author != "" {
					article["author"] = author
					break
				}
			}

			// Date
			dateSelectors := []string{"time", "[itemprop='datePublished']", ".date", ".published", ".post-date"}
			for _, ds := range dateSelectors {
				el := s.Find(ds).First()
				if datetime, ok := el.Attr("datetime"); ok {
					article["date"] = datetime
					break
				}
				if text := strings.TrimSpace(el.Text()); text != "" {
					article["date"] = text
					break
				}
			}

			// Content preview
			if content := strings.TrimSpace(s.Find("p").First().Text()); content != "" && len(content) > 20 {
				if len(content) > 500 {
					content = content[:500] + "..."
				}
				article["content_preview"] = content
			}

			if len(article) > 1 {
				result.Data = append(result.Data, article)
			}
		})
	}
}

// extractLinks extracts all meaningful links from the page.
func (ae *AutoExtractor) extractLinks(doc *goquery.Document, result *ExtractedData) {
	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		text := strings.TrimSpace(s.Text())
		rel, _ := s.Attr("rel")

		if href != "" && href != "#" && !strings.HasPrefix(href, "javascript:") {
			result.Links = append(result.Links, ExtractedLink{
				Text: text,
				URL:  href,
				Rel:  rel,
			})
		}
	})
}

// extractImages extracts all images from the page.
func (ae *AutoExtractor) extractImages(doc *goquery.Document, result *ExtractedData) {
	doc.Find("img[src]").Each(func(i int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		alt, _ := s.Attr("alt")

		if src != "" {
			result.Images = append(result.Images, ExtractedImage{
				URL: src,
				Alt: alt,
			})
		}
	})
}

// classifyPage determines the page type based on extracted data.
func (ae *AutoExtractor) classifyPage(result *ExtractedData) {
	productCount := 0
	articleCount := 0

	for _, item := range result.Data {
		if t, ok := item["_type"]; ok {
			switch t {
			case "product":
				productCount++
			case "article":
				articleCount++
			}
		}

		// Check JSON-LD @type
		if t, ok := item["@type"]; ok {
			switch fmt.Sprintf("%v", t) {
			case "Product":
				productCount++
			case "Article", "NewsArticle", "BlogPosting":
				articleCount++
			}
		}
	}

	switch {
	case productCount >= 3:
		result.Type = "listing"
	case productCount > 0:
		result.Type = "product"
	case articleCount > 0:
		result.Type = "article"
	case len(result.Tables) > 0:
		result.Type = "data"
	default:
		result.Type = "generic"
	}
}

// ExtractToJSON extracts data and writes it as formatted JSON to stdout.
func (ae *AutoExtractor) ExtractToJSON(resp *types.Response) error {
	data, err := ae.Extract(resp)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// ExtractToFile extracts data and writes it to a file.
func (ae *AutoExtractor) ExtractToFile(resp *types.Response, path string) error {
	data, err := ae.Extract(resp)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

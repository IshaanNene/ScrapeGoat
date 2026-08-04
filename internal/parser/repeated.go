package parser

import (
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Tuning for repeated-structure detection. These are thresholds, not magic: each
// one trades false positives against missed listings, and the golden corpus is
// where a change to any of them shows up.
const (
	// minRepeats is how many sibling elements must share a signature before the
	// group is considered a listing. Two is a coincidence; three is a pattern.
	minRepeats = 3

	// minSignalRatio is the fraction of candidates that must carry a price or a
	// link for the group to count. Below this, it is navigation or a footer.
	minSignalRatio = 0.6

	// maxNameLen guards against a whole card's text being taken as its name when
	// the item has no heading.
	maxNameLen = 200
)

// detectRepeatedItems finds product-like listings by structure rather than by
// guessing class names.
//
// The selector-based extractor matches a fixed list — `.product`, `.product-card`,
// `.item-card` and friends — which only works when a site happens to use the class
// names the list anticipates. books.toscrape.com, the canonical scraping test site,
// uses `article.product_pod`; the underscore alone was enough to make the headline
// example return nothing.
//
// Adding `.product_pod` to the list would fix that one site and leave the next
// hundred broken. What a product grid actually *is* — independent of naming — is a
// set of sibling elements with the same shape, most of which contain a price and a
// link. That is what this detects.
func detectRepeatedItems(doc *goquery.Document) []map[string]any {
	var best []*goquery.Selection
	bestScore := 0.0

	doc.Find("*").Each(func(_ int, container *goquery.Selection) {
		groups := groupChildrenBySignature(container)

		for _, group := range groups {
			if len(group) < minRepeats {
				continue
			}

			score := scoreGroup(group)
			// Prefer the higher-scoring group; break ties by size, so a page with
			// both a nav bar and a product grid picks the grid.
			if score > bestScore || (score == bestScore && len(group) > len(best)) {
				bestScore, best = score, group
			}
		}
	})

	if bestScore < minSignalRatio || len(best) < minRepeats {
		return nil
	}

	items := make([]map[string]any, 0, len(best))
	for _, sel := range best {
		if item := itemFromSelection(sel); item != nil {
			items = append(items, item)
		}
	}
	return items
}

// groupChildrenBySignature buckets a container's direct children by tag name and
// class set, so structurally identical siblings land together.
func groupChildrenBySignature(container *goquery.Selection) map[string][]*goquery.Selection {
	groups := make(map[string][]*goquery.Selection)

	container.Children().Each(func(_ int, child *goquery.Selection) {
		sig := signatureOf(child)
		if sig == "" {
			return
		}
		groups[sig] = append(groups[sig], child)
	})

	return groups
}

// signatureOf renders an element's structural identity: its tag plus its sorted
// class list. Sorted because class order is not meaningful, and two elements that
// differ only in attribute order are the same shape.
func signatureOf(s *goquery.Selection) string {
	if len(s.Nodes) == 0 {
		return ""
	}
	tag := goquery.NodeName(s)

	classes := strings.Fields(s.AttrOr("class", ""))
	sort.Strings(classes)

	return tag + "." + strings.Join(classes, ".")
}

// scoreGroup returns the fraction of a group's members that look like listing
// entries — that is, carry a price or a link plus some text.
func scoreGroup(group []*goquery.Selection) float64 {
	if len(group) == 0 {
		return 0
	}

	hits := 0
	for _, sel := range group {
		text := sel.Text()

		hasPrice := pricePattern.MatchString(text)
		hasLink := sel.Find("a[href]").Length() > 0
		hasContent := len(strings.TrimSpace(text)) > 10

		// A price is the strongest signal a listing entry can give. A link alone
		// is not enough — that would match every navigation menu on the web — so
		// it must come with an image or a heading, which is what a card has and a
		// nav item does not.
		switch {
		case hasPrice && hasContent:
			hits++
		case hasLink && hasContent && (sel.Find("img").Length() > 0 || hasHeading(sel)):
			hits++
		}
	}

	return float64(hits) / float64(len(group))
}

func hasHeading(s *goquery.Selection) bool {
	return s.Find("h1, h2, h3, h4, h5, h6").Length() > 0
}

// itemFromSelection pulls the fields a listing entry usually carries. Missing
// fields are omitted rather than emitted empty, so a consumer can tell "absent"
// from "present but blank".
func itemFromSelection(s *goquery.Selection) map[string]any {
	item := map[string]any{"_type": "product"}

	// Name: prefer a heading, then a link's title attribute, then link text.
	if h := s.Find("h1, h2, h3, h4, h5, h6").First(); h.Length() > 0 {
		if name := cleanText(h.Text()); name != "" {
			// A heading often wraps the link whose title attribute holds the
			// untruncated name — books.toscrape.com truncates the visible text
			// with an ellipsis and keeps the full title on the anchor.
			if title := cleanText(h.Find("a").AttrOr("title", "")); title != "" {
				item["name"] = title
			} else {
				item["name"] = name
			}
		}
	}
	if _, ok := item["name"]; !ok {
		if a := s.Find("a[title]").First(); a.Length() > 0 {
			if title := cleanText(a.AttrOr("title", "")); title != "" {
				item["name"] = title
			}
		}
	}
	if _, ok := item["name"]; !ok {
		if a := s.Find("a").First(); a.Length() > 0 {
			if name := cleanText(a.Text()); name != "" && len(name) < maxNameLen {
				item["name"] = name
			}
		}
	}

	if price := pricePattern.FindString(s.Text()); price != "" {
		item["price"] = cleanText(price)
	}

	if img := s.Find("img").First(); img.Length() > 0 {
		if src, ok := img.Attr("src"); ok {
			item["image"] = src
		}
		if alt := cleanText(img.AttrOr("alt", "")); alt != "" {
			item["image_alt"] = alt
		}
	}

	if href, ok := s.Find("a[href]").First().Attr("href"); ok {
		item["url"] = href
	}

	// A bare _type with nothing attached is noise, not a result.
	if len(item) < 3 {
		return nil
	}
	return item
}

// cleanText collapses the whitespace that markup indentation leaves behind.
func cleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

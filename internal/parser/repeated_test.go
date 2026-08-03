package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func docFrom(t *testing.T, html string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	return doc
}

// TestDetectsListingWithUnguessableClassNames is the regression test for the
// headline example returning nothing.
//
// `scrapegoat extract https://books.toscrape.com` — the first command in the
// README — reported `"type": "generic"` and `"data": []`. The selector list
// guessed `.product`, `.product-card`, `.item-card`; the site uses
// `article.product_pod`. One underscore.
//
// Adding `.product_pod` to the list would have fixed that site and left the next
// hundred broken, so the fix is structural: repeated siblings of the same shape,
// most carrying a price and a link.
func TestDetectsListingWithUnguessableClassNames(t *testing.T) {
	doc := docFrom(t, `
	<html><body><ol class="row">
	  <li><article class="product_pod">
	    <h3><a href="/one" title="A Light in the Attic">A Light in the ...</a></h3>
	    <p class="price_color">£51.77</p>
	    <img src="/one.jpg" alt="A Light in the Attic">
	  </article></li>
	  <li><article class="product_pod">
	    <h3><a href="/two" title="Tipping the Velvet">Tipping the ...</a></h3>
	    <p class="price_color">£53.74</p>
	    <img src="/two.jpg" alt="Tipping the Velvet">
	  </article></li>
	  <li><article class="product_pod">
	    <h3><a href="/three" title="Soumission">Soumission</a></h3>
	    <p class="price_color">£50.10</p>
	    <img src="/three.jpg" alt="Soumission">
	  </article></li>
	</ol></body></html>`)

	items := detectRepeatedItems(doc)

	if len(items) != 3 {
		t.Fatalf("detected %d items, want 3", len(items))
	}

	// The full name lives on the anchor's title attribute; the visible text is
	// truncated with an ellipsis. Taking the visible text would silently produce
	// a corpus of truncated titles.
	if got := items[0]["name"]; got != "A Light in the Attic" {
		t.Errorf("name = %v, want the untruncated title attribute", got)
	}
	if got := items[0]["price"]; got != "£51.77" {
		t.Errorf("price = %v, want £51.77", got)
	}
	if got := items[0]["url"]; got != "/one" {
		t.Errorf("url = %v, want /one", got)
	}
}

// TestNavigationIsNotAListing is the false-positive guard. Repeated sibling links
// are the single most common structure on the web, and treating them as products
// would make the extractor worse than useless — it would fill a corpus with menu
// items.
func TestNavigationIsNotAListing(t *testing.T) {
	doc := docFrom(t, `
	<html><body>
	<nav><ul>
	  <li><a href="/">Home</a></li>
	  <li><a href="/books">Books</a></li>
	  <li><a href="/about">About</a></li>
	  <li><a href="/contact">Contact</a></li>
	  <li><a href="/help">Help</a></li>
	</ul></nav>
	<footer><ul>
	  <li><a href="/tos">Terms</a></li>
	  <li><a href="/privacy">Privacy</a></li>
	  <li><a href="/cookies">Cookies</a></li>
	</ul></footer>
	</body></html>`)

	if items := detectRepeatedItems(doc); len(items) != 0 {
		t.Errorf("detected %d items in a page containing only navigation: %v", len(items), items)
	}
}

// TestListingWinsOverNavigation checks the scoring picks the right group when a
// page contains both — which every real shop does.
func TestListingWinsOverNavigation(t *testing.T) {
	doc := docFrom(t, `
	<html><body>
	<nav><ul>
	  <li><a href="/">Home</a></li>
	  <li><a href="/books">Books</a></li>
	  <li><a href="/about">About</a></li>
	</ul></nav>
	<div class="grid">
	  <div class="c"><h3><a href="/a">Item A</a></h3><span>$10.00</span></div>
	  <div class="c"><h3><a href="/b">Item B</a></h3><span>$20.00</span></div>
	  <div class="c"><h3><a href="/c">Item C</a></h3><span>$30.00</span></div>
	</div>
	</body></html>`)

	items := detectRepeatedItems(doc)
	if len(items) != 3 {
		t.Fatalf("detected %d items, want the 3 priced cards", len(items))
	}
	for _, item := range items {
		if _, ok := item["price"]; !ok {
			t.Errorf("picked an item with no price, so it chose the nav: %v", item)
		}
	}
}

func TestBelowThresholdIsNotAListing(t *testing.T) {
	// Two repeats is a coincidence, not a pattern.
	doc := docFrom(t, `
	<html><body><div>
	  <div class="c"><h3><a href="/a">A</a></h3><span>$10.00</span></div>
	  <div class="c"><h3><a href="/b">B</a></h3><span>$20.00</span></div>
	</div></body></html>`)

	if items := detectRepeatedItems(doc); len(items) != 0 {
		t.Errorf("detected %d items from only 2 repeats, want 0", len(items))
	}
}

func TestEmptyAndDegenerateInput(t *testing.T) {
	for _, html := range []string{
		``,
		`<html></html>`,
		`<html><body></body></html>`,
		`<div><div></div><div></div><div></div></div>`,
	} {
		if items := detectRepeatedItems(docFrom(t, html)); len(items) != 0 {
			t.Errorf("detected %d items in %q", len(items), html)
		}
	}
}

// TestAutoExtractorFindsTheListingEndToEnd checks the wiring, not just the
// detector: the fallback must actually run when the selector pass finds nothing,
// and the page must classify as a listing.
func TestAutoExtractorFindsTheListingEndToEnd(t *testing.T) {
	page := filepath.Join("testdata", "pages", "13_product_listing.html")
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("read corpus page: %v", err)
	}

	ae := NewAutoExtractor(fuzzLogger)
	result, err := ae.Extract(mkResponse(t, body))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	products := 0
	for _, item := range result.Data {
		if item["_type"] == "product" {
			products++
		}
	}

	if products != 3 {
		t.Fatalf("extracted %d products, want 3 (result type %q)", products, result.Type)
	}
	if result.Type != "listing" {
		t.Errorf("page type = %q, want listing", result.Type)
	}
}

// TestSelectorPassStillWins checks the fallback does not second-guess a site that
// marks its products up properly.
func TestSelectorPassStillWins(t *testing.T) {
	doc := docFrom(t, `
	<html><body>
	  <div class="product"><h3>Marked Up</h3><span class="price">$1.00</span></div>
	  <div class="product"><h3>Also Marked Up</h3><span class="price">$2.00</span></div>
	  <div class="product"><h3>Third</h3><span class="price">$3.00</span></div>
	</body></html>`)

	ae := NewAutoExtractor(fuzzLogger)
	result := &ExtractedData{}
	ae.extractProducts(doc, result)

	if len(result.Data) != 3 {
		t.Fatalf("selector pass found %d products, want 3", len(result.Data))
	}
}

func TestSignatureIgnoresClassOrder(t *testing.T) {
	a := docFrom(t, `<div class="b a c"></div>`).Find("div")
	b := docFrom(t, `<div class="c b a"></div>`).Find("div")

	if signatureOf(a) != signatureOf(b) {
		t.Errorf("class order changed the signature: %q vs %q", signatureOf(a), signatureOf(b))
	}
}

func FuzzDetectRepeatedItems(f *testing.F) {
	f.Add(`<div><div class="c"><a href="/a">A</a><span>$1.00</span></div></div>`)
	f.Add(`<nav><ul><li><a href="/">H</a></li></ul></nav>`)
	f.Add(``)
	f.Add(`<<<>>>`)

	f.Fuzz(func(t *testing.T, html string) {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		if err != nil {
			return
		}
		// Must not panic, and every emitted item must carry more than its type
		// tag — a bare {"_type":"product"} is noise in a dataset.
		for _, item := range detectRepeatedItems(doc) {
			if len(item) < 3 {
				t.Fatalf("emitted a near-empty item: %v", item)
			}
		}
	})
}

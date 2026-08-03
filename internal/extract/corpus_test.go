package extract

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// The benchmark corpus.
//
// # Where ground truth comes from
//
// Three options were available, and each trades credibility against effort:
//
//  1. Hand-annotate real pages. Highest fidelity, but slow, subjective at the
//     margins (is the author byline content?), and the pages cannot be committed
//     without inheriting their copyright.
//  2. Import an existing benchmark (CleanEval, dragnet, the trafilatura eval set).
//     Credible and comparable, but the corpora carry their own licences and are
//     not ours to redistribute.
//  3. Compose pages from known article text and known boilerplate. Ground truth is
//     exact *by construction* rather than by judgement, the corpus is committed as
//     code so anyone can reproduce it, and difficulty can be varied deliberately
//     rather than hoping the sample happens to cover a hard case.
//
// This uses (3), and the weakness is worth stating plainly: composed pages are
// tidier than the real web. A synthetic corpus measures whether an extractor
// handles the *structures* it contains; it cannot tell you how the extractor
// behaves on markup nobody would write on purpose. Results here are a lower bound
// on difficulty, not a claim about real-world accuracy — which is why
// docs/EXTRACTION.md says so and lists real-page evaluation as outstanding.
//
// Generated deterministically from a fixed seed, so the numbers in the docs are
// reproducible.

// Document is one corpus page with its known main content.
type Document struct {
	Name string
	HTML string

	// Want is the main-content text, exactly as composed. Whitespace-normalised,
	// since no extractor should be judged on indentation.
	Want string

	// Tier is how hard the page is meant to be. 1-4 vary the markup idiom; 5-8
	// are hard cases where the difficulty is the *content*, not the tags.
	// Reporting per tier shows where an extractor fails, which a single aggregate
	// number hides.
	Tier int
}

// --- Content ---

// articles are the main content. Deliberately ordinary prose: an extractor should
// not be rewarded for recognising unusual vocabulary.
var articles = []struct {
	title string
	paras []string
}{
	{
		title: "How Bloom Filters Trade Memory for Certainty",
		paras: []string{
			"A Bloom filter answers one question and answers it with a caveat. Asked whether it has seen an item before, it will tell you either that it definitely has not, or that it probably has. There is no third answer and no way to get one.",
			"That asymmetry is the whole design. The filter stores a bit array and a handful of hash functions rather than the items themselves, so its memory cost is fixed by how many items you expect rather than how large those items are. A million URLs cost the same as a million sentences.",
			"The price is false positives. An item that was never added can hash into bits that other items already set, and the filter will report it as seen. The rate depends on the size of the array relative to the number of insertions, and it rises as the filter fills.",
			"For deduplication during a crawl, that trade is usually acceptable and occasionally not. A false positive means a page is treated as already visited and never fetched. If you are building a search index, losing one page in a hundred is a rounding error. If you are collecting evidence, it is a defect.",
		},
	},
	{
		title: "Why Politeness Delays Are Harder Than They Look",
		paras: []string{
			"The naive implementation of crawler politeness is a sleep between requests to the same host. It is easy to write, easy to reason about, and wrong in a way that only shows up under concurrency.",
			"The problem is where the sleep happens. If a worker takes a request from the queue and then discovers it must wait, that worker is occupied for the duration of the wait. With enough workers pointed at one slow host, the entire pool ends up asleep while other hosts have work ready and nobody to do it.",
			"The fix is to move the decision earlier. A worker should ask whether a host is ready before committing to a request for it, and skip past to something runnable if not. That turns the delay from a property of the worker into a property of the queue.",
			"None of this is novel. Scrapy has had per-domain slots for years, and the literature on polite crawling predates it. It is simply a place where the obvious implementation and the correct one look similar enough that the difference is easy to miss until throughput does not match the configuration.",
		},
	},
	{
		title: "Reproducibility Is a Property of Systems, Not Intentions",
		paras: []string{
			"A pipeline that produces different output on identical input is not merely inconvenient. It is a pipeline whose results cannot be checked, by anyone, including the person who wrote it.",
			"The usual sources are mundane. Wall-clock timestamps embedded in records. Iteration over a hash map whose order the language randomises on purpose. Concurrency that interleaves differently depending on machine load. None of these look like bugs during development, because a single run always looks fine.",
			"Removing them is unglamorous and mostly mechanical: inject the clock, seed the randomness, sort before you serialise. The difficulty is not any individual fix but the discipline, because a single new call to the system clock quietly undoes the property for everything downstream.",
			"What it buys is disproportionate. Bugs become reproducible instead of anecdotal. Two runs become comparable, so a change can be measured rather than argued about. And the output becomes something a third party can verify rather than trust.",
		},
	},
}

// --- Boilerplate ---
//
// The blocks a real page surrounds its content with. These must never appear in
// the extracted text; every token of them that does is a precision loss.

var (
	navLinks = []string{"Home", "About", "Archive", "Topics", "Newsletter", "Contact", "Search", "Log in"}

	sidebarItems = []string{
		"Subscribe to the weekly digest",
		"Follow updates by RSS",
		"Sponsored: monitoring that scales",
		"Popular this month",
		"Browse the archive by year",
	}

	relatedTitles = []string{
		"Five things nobody tells you about caching",
		"The case against microservices, revisited",
		"An introduction to consistent hashing",
		"What we learned migrating to Postgres",
		"Notes on rate limiting at the edge",
	}

	comments = []string{
		"Great write-up, thanks for sharing this.",
		"I disagree with the second point but the rest is solid.",
		"Has anyone tried this approach at larger scale? Curious how it holds up.",
		"First!",
		"There is a typo in paragraph three.",
		"This helped me debug something at work today, much appreciated.",
	}

	cookieBanner = "We use cookies and similar technologies to personalise content, measure traffic, and improve your experience. By continuing to browse you agree to our use of cookies. Manage preferences or accept all."

	footerText = "© 2026 Example Media Group. All rights reserved. Terms of service. Privacy policy. Do not sell my personal information. Careers. Advertise with us."
)

// --- Markup styles ---

// renderContent renders the article in one of four markup idioms. Having several
// is the point: an extractor that only recognises <article> should score well on
// tier 1 and badly on tiers 3 and 4, and the report should show that rather than
// averaging it away.
func renderContent(style int, title string, paras []string) string {
	body := ""
	for _, p := range paras {
		body += "      <p>" + p + "</p>\n"
	}

	switch style {
	case 1: // semantic HTML5
		return "<main>\n    <article>\n      <h1>" + title + "</h1>\n" + body + "    </article>\n  </main>\n"
	case 2: // divs with conventional class names
		return `<div class="post">` + "\n" + `    <div class="entry-content">` + "\n" +
			"      <h1>" + title + "</h1>\n" + body + "    </div>\n  </div>\n"
	case 3: // anonymous divs, no hints at all
		return "<div>\n    <div>\n      <h1>" + title + "</h1>\n" + body + "    </div>\n  </div>\n"
	default: // anonymous divs with misleading class names
		return `<div class="sidebar-widget">` + "\n" + `    <div class="ad-container">` + "\n" +
			"      <h1>" + title + "</h1>\n" + body + "    </div>\n  </div>\n"
	}
}

func renderNav() string {
	items := ""
	for _, l := range navLinks {
		items += `      <li><a href="/` + strings.ToLower(l) + `">` + l + "</a></li>\n"
	}
	return "<nav>\n    <ul>\n" + items + "    </ul>\n  </nav>\n"
}

func renderSidebar() string {
	items := ""
	for _, s := range sidebarItems {
		items += `      <li><a href="#">` + s + "</a></li>\n"
	}
	return `<aside class="sidebar">` + "\n    <ul>\n" + items + "    </ul>\n  </aside>\n"
}

func renderRelated() string {
	items := ""
	for _, r := range relatedTitles {
		items += `      <li><a href="/post">` + r + "</a></li>\n"
	}
	return `<div class="related-posts">` + "\n    <h3>Related</h3>\n    <ul>\n" + items + "    </ul>\n  </div>\n"
}

// renderComments is the adversarial block: comment threads are prose, not links,
// so link density does not separate them from the article. Length is the tell —
// and on many real pages the comments are longer than the post.
func renderComments(repeat int) string {
	out := `<section class="comments">` + "\n    <h3>Comments</h3>\n"
	for i := 0; i < repeat; i++ {
		for j, c := range comments {
			out += `    <div class="comment"><p>` + c + "</p>" +
				`<span class="meta">user` + fmt.Sprint(i*len(comments)+j) + " · 2 days ago</span></div>\n"
		}
	}
	return out + "  </section>\n"
}

func renderCookieBanner() string {
	return `<div class="cookie-consent"><p>` + cookieBanner + `</p>` +
		`<button>Accept all</button><button>Manage</button></div>` + "\n"
}

func renderFooter() string {
	return "<footer>\n    <p>" + footerText + "</p>\n  </footer>\n"
}

// buildCorpus composes the documents. Deterministic: same seed, same corpus, so
// the numbers in docs/EXTRACTION.md can be reproduced exactly.
func buildCorpus() []Document {
	rng := rand.New(rand.NewPCG(20260803, 1))
	var docs []Document

	for style := 1; style <= 4; style++ {
		for ai, art := range articles {
			content := renderContent(style, art.title, art.paras)

			// Boilerplate volume grows with the tier, so tier 4 pages are mostly
			// not content — which is the realistic case.
			commentBlocks := 1
			if style >= 3 {
				commentBlocks = 3
			}

			var b strings.Builder
			b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
			b.WriteString("  <meta charset=\"utf-8\">\n  <title>" + art.title + " | Example Media</title>\n")
			b.WriteString("</head>\n<body>\n  ")
			b.WriteString(renderCookieBanner())
			b.WriteString("  " + renderNav())

			// Vary whether the sidebar precedes or follows the content, so an
			// extractor cannot win by always taking the second block.
			if rng.IntN(2) == 0 {
				b.WriteString("  " + renderSidebar())
				b.WriteString("  " + content)
			} else {
				b.WriteString("  " + content)
				b.WriteString("  " + renderSidebar())
			}

			b.WriteString("  " + renderRelated())
			b.WriteString("  " + renderComments(commentBlocks))
			b.WriteString("  " + renderFooter())
			b.WriteString("</body>\n</html>\n")

			// Ground truth: the title and the paragraphs, nothing else.
			want := art.title + " " + strings.Join(art.paras, " ")

			docs = append(docs, Document{
				Name: fmt.Sprintf("tier%d/article%d", style, ai+1),
				HTML: b.String(),
				Want: normalise(want),
				Tier: style,
			})
		}
	}

	docs = append(docs, hardCases()...)
	return docs
}

// hardCases are the pages where the difficulty is content rather than markup.
//
// Added after the first run of the benchmark returned 1.000 on every tier for the
// density extractor. A benchmark that the tool under test never fails is not
// measuring anything — it is describing the corpus. These are the shapes that
// actually separate extractors, and they were chosen because each defeats a
// different heuristic.
func hardCases() []Document {
	var docs []Document
	art := articles[0]

	// 5. Comments longer than the article.
	//
	// Defeats "take the biggest block of prose". Comments are prose, so link
	// density does not separate them, and here there is more comment text than
	// article text.
	{
		content := renderContent(3, art.title, art.paras)
		var b strings.Builder
		b.WriteString(pageHead(art.title))
		b.WriteString("  " + renderNav())
		b.WriteString("  " + content)
		b.WriteString("  " + renderLongComments(14))
		b.WriteString("  " + renderFooter())
		b.WriteString(pageTail())
		docs = append(docs, Document{
			Name: "tier5/comments-dominate",
			HTML: b.String(),
			Want: normalise(art.title + " " + strings.Join(art.paras, " ")),
			Tier: 5,
		})
	}

	// 6. A short article in heavy boilerplate.
	//
	// Defeats any absolute length threshold. The article is two sentences; every
	// surrounding block is longer than it.
	{
		short := []string{
			"The release is out. It fixes the crash reported last week and nothing else.",
		}
		content := renderContent(3, "Patch Release 2.1.4", short)
		var b strings.Builder
		b.WriteString(pageHead("Patch Release 2.1.4"))
		b.WriteString("  " + renderCookieBanner())
		b.WriteString("  " + renderNav())
		b.WriteString("  " + renderSidebar())
		b.WriteString("  " + content)
		b.WriteString("  " + renderRelated())
		b.WriteString("  " + renderLongComments(6))
		b.WriteString("  " + renderFooter())
		b.WriteString(pageTail())
		docs = append(docs, Document{
			Name: "tier6/short-article",
			HTML: b.String(),
			Want: normalise("Patch Release 2.1.4 " + strings.Join(short, " ")),
			Tier: 6,
		})
	}

	// 7. An article split by inline ad blocks.
	//
	// Defeats "return the single best node". The paragraphs live in separate
	// containers, so taking only the winner truncates the article.
	{
		var body strings.Builder
		body.WriteString("<div>\n  <div><h1>" + art.title + "</h1></div>\n")
		for i, p := range art.paras {
			body.WriteString("  <div class=\"para-block\"><p>" + p + "</p></div>\n")
			if i == 1 {
				body.WriteString(`  <div class="ad"><p>Advertisement. Try our new monitoring product free for thirty days, no card required.</p></div>` + "\n")
			}
		}
		body.WriteString("</div>\n")

		var b strings.Builder
		b.WriteString(pageHead(art.title))
		b.WriteString("  " + renderNav())
		b.WriteString("  " + body.String())
		b.WriteString("  " + renderFooter())
		b.WriteString(pageTail())
		docs = append(docs, Document{
			Name: "tier7/split-by-ads",
			HTML: b.String(),
			Want: normalise(art.title + " " + strings.Join(art.paras, " ")),
			Tier: 7,
		})
	}

	// 8. A page with no article at all.
	//
	// A link directory. The correct answer is little or nothing; an extractor that
	// confidently returns the nav bar here is worse than one that returns empty,
	// and only a corpus containing this case can tell them apart.
	{
		var b strings.Builder
		b.WriteString(pageHead("Directory"))
		b.WriteString("  " + renderNav())
		b.WriteString("  " + renderSidebar())
		b.WriteString("  " + renderRelated())
		b.WriteString("  " + renderFooter())
		b.WriteString(pageTail())
		docs = append(docs, Document{
			Name: "tier8/no-content",
			HTML: b.String(),
			Want: "",
			Tier: 8,
		})
	}

	return docs
}

func pageHead(title string) string {
	return "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n  <meta charset=\"utf-8\">\n  <title>" +
		title + " | Example Media</title>\n</head>\n<body>\n"
}

func pageTail() string { return "</body>\n</html>\n" }

// renderLongComments produces comment threads whose entries are full paragraphs
// rather than one-liners — the case where comments look like article prose.
func renderLongComments(n int) string {
	long := []string{
		"I have been running something similar in production for about eighteen months and the behaviour matches what is described here almost exactly, including the part about the failure only showing up under sustained load rather than in testing.",
		"This is a good summary but I think it understates how much the choice depends on what you are optimising for. In our case the memory saving was irrelevant and the latency cost was not, so we went the other way entirely.",
		"Worth adding that the same reasoning applies to the write path, which the article does not cover. We spent a long time debugging something that turned out to be this exact issue in a different subsystem.",
	}

	out := `<section class="comments">` + "\n    <h3>Comments</h3>\n"
	for i := 0; i < n; i++ {
		c := long[i%len(long)]
		out += `    <div class="comment"><p>` + c + "</p></div>\n"
	}
	return out + "  </section>\n"
}

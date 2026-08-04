// Package extract finds the main content of a web page.
//
// # The problem
//
// A crawled page is mostly not content. Navigation, sidebars, cookie banners,
// related-article rails, comment threads, and footers routinely outweigh the
// article by an order of magnitude. Everything downstream of extraction —
// deduplication, quality filtering, embedding, training — is
// garbage-in-garbage-out on a bad extractor, so this is the layer that decides
// whether a corpus is worth building.
//
// # Why not selectors
//
// The previous approach matched a fixed list of CSS selectors: `article`,
// `.post`, `.entry-content`, `.blog-post`. That works on sites whose authors
// happened to use those class names and fails silently on everything else,
// returning navigation and cookie text as article prose. The same brittleness
// made `scrapegoat extract` return nothing on books.toscrape.com until the
// listing detector replaced its selector list with structural detection.
//
// This package scores DOM nodes instead: text density, link density, and
// structural position. Those signals do not depend on what anyone named a class.
//
// # On measurement
//
// An extractor's quality claim is only as good as its evaluation, and the
// evaluation is the harder half. See extract_bench_test.go for the harness and
// docs/EXTRACTION.md for results — including the cases where this loses.
package extract

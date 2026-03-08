# Examples

## 1. Quotes Spider

Scrapes quotes from [quotes.toscrape.com](https://quotes.toscrape.com).

```bash
go run ./examples/quotes_spider/
```

**Output:** Quote text, author, tags for each page with pagination.

---

## 2. Product Scraper

Scrapes product listings with prices, ratings, and images.

```bash
go run ./examples/product_scraper/
```

**Output:** Product name, price, availability, image URL, rating.

---

## 3. News Crawler

Extracts article titles, summaries, and authors from news sites.

```bash
go run ./examples/news_crawler/
```

---

## 4. E-Commerce Monitor

Monitors product prices and detects changes over time.

```bash
go run ./examples/ecommerce_monitor/
```

---

## 5. API Scraper

Makes API calls and extracts JSON data.

```bash
go run ./examples/api_scraper/
```

---

## 6. Multi-Page Crawler

Crawls multiple pages using pagination and link following.

```bash
go run ./examples/multi_page/
```

---

## 7. Dynamic Content

Uses browser automation for JavaScript-rendered pages.

```bash
go run ./examples/dynamic_content/
```

---

## 8. LinkedIn Scraper

Profile-style data extraction (demo using quotes.toscrape.com).

```bash
go run ./examples/linkedin_scraper/
```

---

## Auto-Extract (No Code Required)

Extract structured data from any URL without writing a spider:

```bash
scrapegoat extract https://books.toscrape.com
```

This auto-detects JSON-LD, OpenGraph, products, articles, and tables.

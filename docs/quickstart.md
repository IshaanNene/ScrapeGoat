# Quick Start Guide

Get ScrapeGoat running in 3 minutes.

## Install

```bash
go install github.com/IshaanNene/ScrapeGoat/cmd/scrapegoat@latest
```

Or build from source:

```bash
git clone https://github.com/IshaanNene/ScrapeGoat.git
cd ScrapeGoat
make build
```

## 1. Create a Project

```bash
scrapegoat new project my_scraper
cd my_scraper
```

This generates:
```
my_scraper/
├── scrapegoat.yaml     # Configuration
├── go.mod              # Go module
└── spiders/
    └── main.go         # Your spider
```

## 2. Edit Your Spider

```go
func (s *MySpider) StartURLs() []string {
    return []string{"https://books.toscrape.com"}
}

func (s *MySpider) Parse(resp *scrapegoat.Response) (*scrapegoat.SpiderResult, error) {
    result := &scrapegoat.SpiderResult{}
    resp.Doc.Find(".product_pod").Each(func(i int, s *goquery.Selection) {
        item := scrapegoat.NewItem(resp.URL)
        item.Set("title", s.Find("h3 a").AttrOr("title", ""))
        item.Set("price", s.Find(".price_color").Text())
        result.Items = append(result.Items, item)
    })
    return result, nil
}
```

## 3. Run

```bash
go run ./spiders/
```

## Quick Commands

| Command | Description |
|---------|-------------|
| `scrapegoat crawl <url>` | Crawl a URL with link following |
| `scrapegoat extract <url>` | Auto-extract structured data |
| `scrapegoat new spider <name>` | Scaffold a new spider |
| `scrapegoat new project <name>` | Scaffold project with config |
| `scrapegoat serve` | Start the REST/WebSocket API server |
| `scrapegoat mcp` | Start the MCP server |
| `scrapegoat replay <dir>` | Re-run a recorded crawl from its fetch log |
| `scrapegoat verify <dir>` | Check a fetch log's integrity |

## Using as a Library

```go
crawler := scrapegoat.NewCrawler(
    scrapegoat.WithConcurrency(5),
    scrapegoat.WithMaxDepth(3),
    scrapegoat.WithOutput("json", "./output"),
)

crawler.OnHTML("h1", func(e *scrapegoat.Element) {
    e.Item.Set("title", e.Text())
})

crawler.Start("https://example.com")
crawler.Wait()
```

## Docker

```bash
docker-compose up -d
scrapegoat crawl https://example.com
```

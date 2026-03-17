#!/usr/bin/env bash
# scripts/compare_tools.sh — Compare ScrapeGoat, Colly, Scrapy, and wrk
# against https://books.toscrape.com (depth=1).
#
# Usage: bash scripts/compare_tools.sh
# Prerequisites:
#   go, python3+scrapy, wrk
#   go install github.com/gocolly/colly/v2@latest  (for colly_bench)

set -euo pipefail

TARGET="https://books.toscrape.com"
DEPTH=1
RESULTS_DIR="/tmp/scrapegoat_comparison"
mkdir -p "$RESULTS_DIR"

divider() { printf '=%.0s' {1..70}; echo; }

# ---------------------------------------------------------------------------
# Helper: measure wall time + peak RSS (macOS & Linux)
# ---------------------------------------------------------------------------
measure() {
    local label="$1"; shift
    local outfile="$RESULTS_DIR/${label}.stat"

    echo "[*] Running $label ..."

    if command -v gtime &>/dev/null; then
        # macOS with GNU time (brew install gnu-time)
        gtime -v "$@" 2>"$outfile" || true
    elif [[ "$(uname)" == "Darwin" ]]; then
        # macOS fallback: /usr/bin/time is BSD-flavoured
        local start_ts end_ts
        start_ts=$(python3 -c "import time; print(time.time())")
        "$@" 2>/dev/null || true
        end_ts=$(python3 -c "import time; print(time.time())")
        wall=$(python3 -c "print(f'{$end_ts - $start_ts:.2f}')")
        echo "wall_time_seconds: $wall" > "$outfile"
        echo "peak_rss_kb: N/A (install gnu-time for RSS)" >> "$outfile"
        return
    else
        /usr/bin/time -v "$@" 2>"$outfile" || true
    fi
}

extract_wall() {
    local f="$RESULTS_DIR/${1}.stat"
    if grep -q "Elapsed (wall clock)" "$f" 2>/dev/null; then
        grep "Elapsed (wall clock)" "$f" | awk '{print $NF}'
    elif grep -q "wall_time_seconds" "$f" 2>/dev/null; then
        grep "wall_time_seconds" "$f" | awk '{print $2 "s"}'
    else
        echo "N/A"
    fi
}

extract_rss() {
    local f="$RESULTS_DIR/${1}.stat"
    if grep -q "Maximum resident set size" "$f" 2>/dev/null; then
        local kb
        kb=$(grep "Maximum resident set size" "$f" | awk '{print $NF}')
        # On macOS, /usr/bin/time reports bytes; on Linux, KB.
        if [[ "$(uname)" == "Darwin" ]]; then
            echo "$((kb / 1024)) KB"
        else
            echo "${kb} KB"
        fi
    elif grep -q "peak_rss_kb" "$f" 2>/dev/null; then
        grep "peak_rss_kb" "$f" | awk '{print $2}'
    else
        echo "N/A"
    fi
}

# ---------------------------------------------------------------------------
# 1. wrk — raw HTTP throughput ceiling (10s burst)
# ---------------------------------------------------------------------------
if command -v wrk &>/dev/null; then
    echo "[*] Running wrk (raw HTTP baseline, 10s, 10 threads, 50 connections)..."
    wrk -t10 -c50 -d10s "$TARGET/" > "$RESULTS_DIR/wrk.out" 2>&1 || true
    WRK_RPS=$(grep "Requests/sec" "$RESULTS_DIR/wrk.out" | awk '{print $2}')
    WRK_LATENCY=$(grep "Latency" "$RESULTS_DIR/wrk.out" | awk '{print $2}')
else
    echo "[!] wrk not found — skipping raw HTTP ceiling test"
    WRK_RPS="N/A (wrk not installed)"
    WRK_LATENCY="N/A"
fi

# ---------------------------------------------------------------------------
# 2. ScrapeGoat
# ---------------------------------------------------------------------------
SCRAPEGOAT_BIN="./scrapegoat"
if [[ ! -x "$SCRAPEGOAT_BIN" ]]; then
    echo "[*] Building ScrapeGoat..."
    go build -o "$SCRAPEGOAT_BIN" ./cmd/scrapegoat/ 2>/dev/null || {
        echo "[!] ScrapeGoat build failed"
        SCRAPEGOAT_BIN=""
    }
fi

if [[ -n "$SCRAPEGOAT_BIN" ]]; then
    measure "scrapegoat" "$SCRAPEGOAT_BIN" crawl "$TARGET" \
        --depth "$DEPTH" --concurrency 10 --output /tmp/sg_out --format json
fi

# ---------------------------------------------------------------------------
# 3. Colly (Go)
# ---------------------------------------------------------------------------
COLLY_BENCH="/tmp/colly_bench"
cat > /tmp/colly_bench_main.go <<'COLLYEOF'
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/gocolly/colly/v2"
)

func main() {
	start := time.Now()
	count := 0

	c := colly.NewCollector(
		colly.MaxDepth(1),
		colly.Async(true),
	)
	c.Limit(&colly.LimitRule{DomainGlob: "*", Parallelism: 10})

	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		_ = e.Request.Visit(e.Attr("href"))
	})
	c.OnResponse(func(r *colly.Response) {
		count++
	})

	if len(os.Args) > 1 {
		_ = c.Visit(os.Args[1])
	} else {
		_ = c.Visit("https://books.toscrape.com")
	}
	c.Wait()

	fmt.Printf("pages: %d, elapsed: %s\n", count, time.Since(start))
}
COLLYEOF

if command -v go &>/dev/null; then
    echo "[*] Building colly bench..."
    (cd /tmp && go mod init colly_bench 2>/dev/null; go get github.com/gocolly/colly/v2@latest 2>/dev/null; go build -o colly_bench colly_bench_main.go 2>/dev/null) || {
        echo "[!] Colly bench build failed — skipping"
        COLLY_BENCH=""
    }
fi

if [[ -x "$COLLY_BENCH" ]]; then
    measure "colly" "$COLLY_BENCH" "$TARGET"
fi

# ---------------------------------------------------------------------------
# 4. Scrapy (Python)
# ---------------------------------------------------------------------------
SCRAPY_PROJECT="/tmp/scrapy_bench"
if command -v scrapy &>/dev/null; then
    rm -rf "$SCRAPY_PROJECT"
    mkdir -p "$SCRAPY_PROJECT"

    cat > "$SCRAPY_PROJECT/bench_spider.py" <<'SCRAPYEOF'
import scrapy

class BenchSpider(scrapy.Spider):
    name = "bench"
    start_urls = ["https://books.toscrape.com"]
    custom_settings = {
        "DEPTH_LIMIT": 1,
        "CONCURRENT_REQUESTS": 10,
        "LOG_LEVEL": "WARNING",
        "FEEDS": {"/tmp/scrapy_out.json": {"format": "json"}},
    }

    def parse(self, response):
        for link in response.css("a::attr(href)").getall():
            yield response.follow(link, self.parse)
        yield {"url": response.url, "title": response.css("title::text").get()}
SCRAPYEOF

    measure "scrapy" scrapy runspider "$SCRAPY_PROJECT/bench_spider.py" \
        -s LOG_LEVEL=WARNING
else
    echo "[!] scrapy not found — install with: pip install scrapy"
fi

# ---------------------------------------------------------------------------
# Results table
# ---------------------------------------------------------------------------
divider
echo ""
echo "  COMPARISON RESULTS — $TARGET (depth=$DEPTH)"
echo ""
printf "  %-18s  %-16s  %-14s\n" "Tool" "Wall Time" "Peak RSS"
printf "  %-18s  %-16s  %-14s\n" "------------------" "----------------" "--------------"

if [[ -n "${WRK_RPS:-}" && "$WRK_RPS" != "N/A"* ]]; then
    printf "  %-18s  %-16s  %-14s\n" "wrk (ceiling)" "10s / ${WRK_RPS} rps" "latency: ${WRK_LATENCY}"
fi

if [[ -f "$RESULTS_DIR/scrapegoat.stat" ]]; then
    printf "  %-18s  %-16s  %-14s\n" "ScrapeGoat" "$(extract_wall scrapegoat)" "$(extract_rss scrapegoat)"
fi

if [[ -f "$RESULTS_DIR/colly.stat" ]]; then
    printf "  %-18s  %-16s  %-14s\n" "Colly" "$(extract_wall colly)" "$(extract_rss colly)"
fi

if [[ -f "$RESULTS_DIR/scrapy.stat" ]]; then
    printf "  %-18s  %-16s  %-14s\n" "Scrapy" "$(extract_wall scrapy)" "$(extract_rss scrapy)"
fi

echo ""
divider
echo "Raw data in: $RESULTS_DIR/"

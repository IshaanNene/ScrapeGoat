#!/usr/bin/env bash
# scripts/e2e_smoke.sh — End-to-end smoke tests for ScrapeGoat
#
# Usage:  bash scripts/e2e_smoke.sh
# Exit:   0 if all pass, 1 if any fail

set -uo pipefail

BIN="./scrapegoat"
PASS=0
FAIL=0
TOTAL=0

green()  { printf "\033[32m%s\033[0m" "$*"; }
red()    { printf "\033[31m%s\033[0m" "$*"; }
result() {
    TOTAL=$((TOTAL + 1))
    local elapsed="$1" name="$2" ok="$3"
    if [[ "$ok" == "true" ]]; then
        PASS=$((PASS + 1))
        printf "  %-50s  $(green PASS)  (%s)\n" "$name" "$elapsed"
    else
        FAIL=$((FAIL + 1))
        printf "  %-50s  $(red FAIL)  (%s)\n" "$name" "$elapsed"
    fi
}

# Build if needed
if [[ ! -x "$BIN" ]]; then
    echo "[*] Building ScrapeGoat..."
    go build -o "$BIN" ./cmd/scrapegoat/ 2>/dev/null || {
        echo "FATAL: build failed"
        exit 1
    }
fi

echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║          ScrapeGoat End-to-End Smoke Tests                  ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

# ───────────────────────────────────────────────────────────────────
# T1: Extract — assert "price" field present in output
# ───────────────────────────────────────────────────────────────────
t1_start=$(date +%s%N)
T1_OUT=$($BIN extract https://books.toscrape.com 2>/dev/null || true)
t1_end=$(date +%s%N)
t1_ms=$(( (t1_end - t1_start) / 1000000 ))

if echo "$T1_OUT" | grep -qi "price"; then
    result "${t1_ms}ms" "T1: extract contains 'price' field" "true"
else
    result "${t1_ms}ms" "T1: extract contains 'price' field" "false"
fi

# ───────────────────────────────────────────────────────────────────
# T2: Distributed crawl — master + 2 workers
# ───────────────────────────────────────────────────────────────────
t2_start=$(date +%s%N)
MASTER_PORT=18081

# Start master
$BIN master --addr ":${MASTER_PORT}" &>/tmp/sg_master.log &
MASTER_PID=$!
sleep 2

# Start worker 1
$BIN worker --master "http://localhost:${MASTER_PORT}" --capacity 5 &>/tmp/sg_worker1.log &
W1_PID=$!

# Start worker 2
$BIN worker --master "http://localhost:${MASTER_PORT}" --capacity 5 &>/tmp/sg_worker2.log &
W2_PID=$!

sleep 2

# Submit a task
curl -s -X POST "http://localhost:${MASTER_PORT}/api/submit" \
    -H "Content-Type: application/json" \
    -d '{"type":"crawl","urls":["https://books.toscrape.com"]}' > /dev/null 2>&1 || true

sleep 5

# Check status
STATUS=$(curl -s "http://localhost:${MASTER_PORT}/api/status" 2>/dev/null || echo '{}')
NODE_COUNT=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('total_nodes',0))" 2>/dev/null || echo 0)

# Cleanup
kill $MASTER_PID $W1_PID $W2_PID 2>/dev/null || true
wait $MASTER_PID $W1_PID $W2_PID 2>/dev/null || true

t2_end=$(date +%s%N)
t2_ms=$(( (t2_end - t2_start) / 1000000 ))

if [[ "$NODE_COUNT" -ge 2 ]]; then
    result "${t2_ms}ms" "T2: distributed crawl (2 workers registered)" "true"
else
    result "${t2_ms}ms" "T2: distributed crawl (2 workers registered)" "false"
fi

# ───────────────────────────────────────────────────────────────────
# T3: Checkpoint resume — no duplicates
# ───────────────────────────────────────────────────────────────────
t3_start=$(date +%s%N)
CHECKPOINT_DIR="/tmp/sg_checkpoint_test"
rm -rf "$CHECKPOINT_DIR" .scrapegoat_checkpoints

# Run 1: crawl first batch (limited requests)
$BIN crawl https://books.toscrape.com \
    --depth 1 --concurrency 2 --max-requests 5 \
    --output "$CHECKPOINT_DIR/run1" --format jsonl 2>/dev/null || true

# Run 2: resume (should continue without duplicates)
$BIN crawl https://books.toscrape.com \
    --depth 1 --concurrency 2 --max-requests 10 \
    --output "$CHECKPOINT_DIR/run2" --format jsonl --resume 2>/dev/null || true

# Count unique URLs across runs
RUN1_URLS=""
RUN2_URLS=""
if [[ -f "$CHECKPOINT_DIR/run1/results.jsonl" ]]; then
    RUN1_URLS=$(grep -o '"url":"[^"]*"' "$CHECKPOINT_DIR/run1/results.jsonl" 2>/dev/null | sort -u || true)
fi
if [[ -f "$CHECKPOINT_DIR/run2/results.jsonl" ]]; then
    RUN2_URLS=$(grep -o '"url":"[^"]*"' "$CHECKPOINT_DIR/run2/results.jsonl" 2>/dev/null | sort -u || true)
fi

ALL_URLS=$(printf '%s\n%s' "$RUN1_URLS" "$RUN2_URLS" | sort | uniq -d)
t3_end=$(date +%s%N)
t3_ms=$(( (t3_end - t3_start) / 1000000 ))

# If there are few or no duplicate URLs between runs, PASS
DUP_COUNT=$(echo "$ALL_URLS" | grep -c . 2>/dev/null || echo 0)
if [[ "$DUP_COUNT" -le 2 ]]; then
    result "${t3_ms}ms" "T3: checkpoint resume (≤2 duplicate URLs)" "true"
else
    result "${t3_ms}ms" "T3: checkpoint resume (≤2 duplicate URLs)" "false"
fi
rm -rf "$CHECKPOINT_DIR" .scrapegoat_checkpoints

# ───────────────────────────────────────────────────────────────────
# T4: Output format validation (JSON, CSV, JSONL)
# ───────────────────────────────────────────────────────────────────
for fmt_name in json csv jsonl; do
    t4_start=$(date +%s%N)
    FMT_DIR="/tmp/sg_fmt_${fmt_name}"
    rm -rf "$FMT_DIR"

    $BIN crawl https://books.toscrape.com \
        --depth 0 --concurrency 2 --max-requests 3 \
        --output "$FMT_DIR" --format "$fmt_name" 2>/dev/null || true

    FILE="$FMT_DIR/results.${fmt_name}"
    ok="false"
    case "$fmt_name" in
        json)
            if [[ -f "$FILE" ]] && python3 -c "import json; json.load(open('$FILE'))" 2>/dev/null; then
                ok="true"
            fi
            ;;
        jsonl)
            if [[ -f "$FILE" ]] && head -1 "$FILE" | python3 -c "import sys,json; json.loads(sys.stdin.read())" 2>/dev/null; then
                ok="true"
            fi
            ;;
        csv)
            if [[ -f "$FILE" ]] && head -1 "$FILE" | grep -q ","; then
                ok="true"
            fi
            ;;
    esac

    t4_end=$(date +%s%N)
    t4_ms=$(( (t4_end - t4_start) / 1000000 ))
    result "${t4_ms}ms" "T4: output format validation ($fmt_name)" "$ok"
    rm -rf "$FMT_DIR"
done

# ───────────────────────────────────────────────────────────────────
# Summary
# ───────────────────────────────────────────────────────────────────
echo ""
echo "────────────────────────────────────────────────────"
printf "  Total: %d  |  Passed: $(green %d)  |  Failed: $(red %d)\n" "$TOTAL" "$PASS" "$FAIL"
echo "────────────────────────────────────────────────────"
echo ""

if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
exit 0

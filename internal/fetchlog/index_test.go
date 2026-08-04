package fetchlog

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLogAppendAndRead(t *testing.T) {
	dir := t.TempDir()

	log, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}

	for i := 0; i < 3; i++ {
		e := Entry{
			Method:     http.MethodGet,
			URL:        "https://example.com/p" + string(rune('a'+i)),
			StatusCode: 200,
			Digest:     Digest([]byte{byte(i)}),
			FetchedAt:  time.Unix(1700000000+int64(i), 0).UTC(),
			Duration:   time.Duration(i) * time.Millisecond,
		}
		got, err := log.Append(e)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got.Seq != int64(i+1) {
			t.Errorf("entry %d got seq %d, want %d", i, got.Seq, i+1)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("read %d entries, want 3", len(entries))
	}
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			t.Errorf("entry %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
	if entries[0].FetchedAt.Unix() != 1700000000 {
		t.Errorf("timestamp did not survive the round trip: %v", entries[0].FetchedAt)
	}
	if entries[2].Duration != 2*time.Millisecond {
		t.Errorf("duration did not survive the round trip: %v", entries[2].Duration)
	}
}

// Reopening must not restart numbering. Two entries claiming the same position
// would give replay no total order, which is the one thing the sequence is for.
func TestLogResumesSequence(t *testing.T) {
	dir := t.TempDir()

	log, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := log.Append(Entry{URL: "https://example.com/"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	log.Close()

	reopened, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	e, err := reopened.Append(Entry{URL: "https://example.com/after"})
	if err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}
	reopened.Close()

	if e.Seq != 6 {
		t.Errorf("first entry after reopen got seq %d, want 6", e.Seq)
	}

	entries, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	seen := make(map[int64]bool, len(entries))
	for _, e := range entries {
		if seen[e.Seq] {
			t.Fatalf("sequence %d appears twice", e.Seq)
		}
		seen[e.Seq] = true
	}
}

// A crash mid-write truncates the last line. Everything before it is intact and
// must stay readable — that is the property append-only storage exists to give.
func TestLogSurvivesTruncatedTail(t *testing.T) {
	dir := t.TempDir()

	log, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := log.Append(Entry{URL: "https://example.com/", StatusCode: 200}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	log.Close()

	path := filepath.Join(dir, "index.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	// Chop halfway into what would be the fifth line.
	if err := os.WriteFile(path, append(raw, []byte(`{"seq":5,"url":"https://exa`)...), 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	entries, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog on a truncated log: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("read %d entries past a truncated tail, want 4", len(entries))
	}

	// And a reopen must number past the intact entries, not over them.
	reopened, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("reopen truncated log: %v", err)
	}
	e, err := reopened.Append(Entry{URL: "https://example.com/recovered"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	reopened.Close()
	if e.Seq <= 4 {
		t.Errorf("recovery reused seq %d, which is already taken", e.Seq)
	}
}

func TestReadLogMissing(t *testing.T) {
	if _, err := ReadLog(t.TempDir()); err == nil {
		t.Fatal("ReadLog of a directory with no index succeeded")
	}
}

// Fetchers run concurrently, so the ledger does too. Every entry must land and
// no two may share a sequence number.
func TestLogConcurrentAppend(t *testing.T) {
	dir := t.TempDir()

	log, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}

	const workers, each = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := log.Append(Entry{URL: "https://example.com/", StatusCode: 200}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	log.Close()

	entries, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if len(entries) != workers*each {
		t.Fatalf("read %d entries, want %d", len(entries), workers*each)
	}
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d has seq %d: sequence has a gap or a duplicate", i, e.Seq)
		}
	}
}

func TestEntryHeadersRoundTrip(t *testing.T) {
	dir := t.TempDir()

	log, err := OpenLog(dir)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Add("Set-Cookie", "a=1")
	h.Add("Set-Cookie", "b=2")

	if _, err := log.Append(Entry{URL: "https://example.com/", Headers: h, StatusCode: 200}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	log.Close()

	entries, err := ReadLog(dir)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	got := entries[0].Headers
	if got.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("Content-Type came back as %q", got.Get("Content-Type"))
	}
	if len(got.Values("Set-Cookie")) != 2 {
		t.Errorf("repeated header collapsed: %v", got.Values("Set-Cookie"))
	}
}

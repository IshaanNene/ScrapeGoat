package fetchlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one fetch attempt.
//
// Attempts are recorded, not just successes. A crawl that hit three 503s before a
// 200 behaves differently from one that succeeded immediately — different backoff,
// different circuit-breaker state, different timing — so a replay that skipped
// straight to the 200 would not be replaying the crawl that happened. Errors are
// therefore first-class entries with no body.
type Entry struct {
	// Seq is the position in the log. Assigned on append and never reused; it is
	// the tiebreaker that gives replay a total order.
	Seq int64 `json:"seq"`

	Method string `json:"method"`
	URL    string `json:"url"`

	// Attempt distinguishes retries of the same URL. Keyed on together with the
	// URL, so a replay hands back the same *sequence* of responses rather than
	// the same response repeatedly.
	Attempt int `json:"attempt"`

	// Digest addresses the body in the store. Empty when the fetch failed.
	Digest string `json:"digest,omitempty"`

	StatusCode int         `json:"status,omitempty"`
	Headers    http.Header `json:"headers,omitempty"`
	FinalURL   string      `json:"final_url,omitempty"`

	// Err is the fetch error, if any. Stored as text because the point is to
	// reproduce the *outcome*, and error identity does not survive a file anyway.
	Err       string `json:"error,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`

	FetchedAt time.Time     `json:"fetched_at"`
	Duration  time.Duration `json:"duration"`

	// RobotsAllowed records what robots.txt said at fetch time. Part of the
	// provenance story: "were you allowed to take this" is a question that gets
	// asked later, and the answer is only available at the moment of fetching.
	RobotsAllowed bool `json:"robots_allowed"`
}

// key identifies an entry for replay lookup.
func (e Entry) key() attemptKey {
	return attemptKey{method: e.Method, url: e.URL, attempt: e.Attempt}
}

type attemptKey struct {
	method  string
	url     string
	attempt int
}

// Log is the append-only ledger of fetch attempts.
//
// Writes are serialised through a mutex and flushed per entry. Buffering would be
// faster and is the wrong trade here: a log that loses its last N entries on a
// crash cannot be replayed against the crawl that produced it, and the crawl is
// network-bound anyway, so the fsync is not the bottleneck.
type Log struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	seq    int64
	path   string
	closed bool
}

// OpenLog opens or creates the ledger under dir.
func OpenLog(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("fetchlog: create dir: %w", err)
	}

	path := filepath.Join(dir, "index.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("fetchlog: open index: %w", err)
	}

	// Resume numbering from whatever is already there, so appending to an existing
	// log does not restart sequence numbers and silently create two entries that
	// claim the same position.
	seq, err := lastSeq(path)
	if err != nil {
		f.Close()
		return nil, err
	}

	return &Log{f: f, w: bufio.NewWriter(f), seq: seq, path: path}, nil
}

func lastSeq(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("fetchlog: read index: %w", err)
	}
	defer f.Close()

	var last int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			// A truncated final line is what a crash looks like. Stop there rather
			// than failing: the entries before it are intact and usable, which is
			// the property append-only storage exists to provide.
			break
		}
		if e.Seq > last {
			last = e.Seq
		}
	}
	return last, nil
}

// Append records an entry, assigning its sequence number.
func (l *Log) Append(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return e, errors.New("fetchlog: append to a closed log")
	}

	l.seq++
	e.Seq = l.seq

	line, err := json.Marshal(&e)
	if err != nil {
		return e, fmt.Errorf("fetchlog: encode entry: %w", err)
	}

	if _, err := l.w.Write(append(line, '\n')); err != nil {
		return e, fmt.Errorf("fetchlog: write entry: %w", err)
	}
	if err := l.w.Flush(); err != nil {
		return e, fmt.Errorf("fetchlog: flush entry: %w", err)
	}
	return e, nil
}

// Close flushes and closes the ledger. Closing twice is not an error.
//
// Idempotence is not politeness here. A Recorder's lifetime is owned by whoever
// registered it with the engine, and the engine closes the fetchers it holds — so
// the caller that opened the log and the engine that used it both, correctly,
// call Close. Returning "file already closed" to the second one turns a clean
// shutdown into a reported failure.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true

	if err := l.w.Flush(); err != nil {
		l.f.Close()
		return err
	}
	return l.f.Close()
}

// Len returns how many entries have been appended.
func (l *Log) Len() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// ReadLog loads every entry from dir, in sequence order.
func ReadLog(dir string) ([]Entry, error) {
	path := filepath.Join(dir, "index.jsonl")

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("fetchlog: no index at %s", path)
		}
		return nil, fmt.Errorf("fetchlog: open index: %w", err)
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			break // truncated tail, as above
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return entries, fmt.Errorf("fetchlog: scan index: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
	return entries, nil
}

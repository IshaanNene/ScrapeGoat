package provenance

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func sampleRecord(url string) Record {
	return Record{
		SchemaVersion: SchemaVersion,
		URL:           url,
		ContentHash:   "hash-" + url,
		FetchedAt:     mustTime(),
		StatusCode:    200,
		MIMEType:      "text/html",
		Language:      "en",
		RobotsAllowed: true,
	}
}

func TestCorpusRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl")

	w, err := NewCorpusWriter(path)
	if err != nil {
		t.Fatalf("NewCorpusWriter: %v", err)
	}
	for _, u := range []string{"https://a/", "https://b/", "https://c/"} {
		if err := w.Write(sampleRecord(u)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadCorpus(path)
	if err != nil {
		t.Fatalf("ReadCorpus: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d records, want 3", len(got))
	}
	if got[0].URL != "https://a/" {
		t.Errorf("records came back out of order: %s first", got[0].URL)
	}
	if !got[0].FetchedAt.Equal(mustTime()) {
		t.Errorf("timestamp did not survive: %v", got[0].FetchedAt)
	}
}

// A record that cannot answer where it came from is refused, and the refusal is
// counted so the summary can say so rather than the file quietly being short.
func TestCorpusSkipsIncompleteRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl")

	w, err := NewCorpusWriter(path)
	if err != nil {
		t.Fatalf("NewCorpusWriter: %v", err)
	}

	if err := w.Write(sampleRecord("https://good/")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// No hash, and no fetch time.
	if err := w.Write(Record{URL: "https://bad/"}); err != nil {
		t.Fatalf("Write of an incomplete record should not error: %v", err)
	}
	w.Close()

	written, skipped := w.Stats()
	if written != 1 || skipped != 1 {
		t.Errorf("written=%d skipped=%d, want 1 and 1", written, skipped)
	}

	got, err := ReadCorpus(path)
	if err != nil {
		t.Fatalf("ReadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].URL != "https://good/" {
		t.Errorf("the incomplete record reached the file: %+v", got)
	}
}

func TestCorpusFillsSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl")

	w, _ := NewCorpusWriter(path)
	r := sampleRecord("https://a/")
	r.SchemaVersion = 0
	if err := w.Write(r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.Close()

	got, _ := ReadCorpus(path)
	if len(got) != 1 || got[0].SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %d, want %d", got[0].SchemaVersion, SchemaVersion)
	}
}

// An interrupted run leaves a half-written final line. Everything before it is
// intact and must stay readable.
func TestReadCorpusSurvivesTruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl")

	w, _ := NewCorpusWriter(path)
	for _, u := range []string{"https://a/", "https://b/"} {
		w.Write(sampleRecord(u))
	}
	w.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(path, append(raw, []byte(`{"url":"https://c`)...), 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	got, err := ReadCorpus(path)
	if err != nil {
		t.Fatalf("ReadCorpus on a truncated file: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("read %d records past a truncated tail, want 2", len(got))
	}
}

// Crawls are concurrent, so the writer is too. Every record must land exactly
// once.
func TestCorpusConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl")

	w, err := NewCorpusWriter(path)
	if err != nil {
		t.Fatalf("NewCorpusWriter: %v", err)
	}

	const workers, each = 8, 20
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				u := "https://example.com/" + string(rune('a'+id)) + string(rune('0'+j%10))
				if err := w.Write(sampleRecord(u)); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadCorpus(path)
	if err != nil {
		t.Fatalf("ReadCorpus: %v", err)
	}
	if len(got) != workers*each {
		t.Errorf("read %d records, want %d", len(got), workers*each)
	}
	written, _ := w.Stats()
	if written != int64(workers*each) {
		t.Errorf("writer counted %d, want %d", written, workers*each)
	}
}

func TestCorpusCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl")

	w, _ := NewCorpusWriter(path)
	w.Write(sampleRecord("https://a/"))

	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestSummarise(t *testing.T) {
	one := 1

	records := []Record{
		sampleRecord("https://a/"),
		func() Record {
			r := sampleRecord("https://b/")
			r.Signals.NoAI = true
			r.Signals.Licence = "https://creativecommons.org/licenses/by/4.0/"
			return r
		}(),
		func() Record {
			r := sampleRecord("https://c/")
			r.Signals.TDMReservation = &one
			r.AIDirectives = SummariseDirectives(ParseRobots("User-agent: GPTBot\nDisallow: /\n"))
			return r
		}(),
		func() Record {
			r := sampleRecord("https://d/")
			r.RobotsAllowed = false
			return r
		}(),
	}

	s := Summarise(records)

	if s.Records != 4 {
		t.Errorf("records = %d", s.Records)
	}
	if s.Restrictive != 2 {
		t.Errorf("restrictive = %d, want 2 (one noai, one TDM reservation)", s.Restrictive)
	}
	if s.RobotsDisallowed != 1 {
		t.Errorf("robots disallowed = %d, want 1", s.RobotsDisallowed)
	}
	if s.Licensed != 1 || len(s.Licences) != 1 {
		t.Errorf("licensed = %d, licences = %v", s.Licensed, s.Licences)
	}
	if s.AISiteWide != 1 {
		t.Errorf("ai_blocked_sites = %d, want 1", s.AISiteWide)
	}
	if s.MIMETypes["text/html"] != 4 {
		t.Errorf("mime types = %v", s.MIMETypes)
	}
	if s.Languages["en"] != 4 {
		t.Errorf("languages = %v", s.Languages)
	}
}

// A corpus that dropped restricted pages would report zero here and look clean.
// The count existing at all is the point.
func TestSummariseCountsRestrictiveRatherThanHidingIt(t *testing.T) {
	r := sampleRecord("https://a/")
	r.Signals.NoAI = true

	s := Summarise([]Record{r})
	if s.Records != 1 {
		t.Fatalf("the restricted record was dropped: %+v", s)
	}
	if s.Restrictive != 1 {
		t.Error("the restriction was not counted")
	}
}

func TestReadCorpusMissingFile(t *testing.T) {
	if _, err := ReadCorpus(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Error("reading a missing corpus succeeded")
	}
}

func TestCorpusWriterCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "corpus.jsonl")

	w, err := NewCorpusWriter(path)
	if err != nil {
		t.Fatalf("NewCorpusWriter: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent directory was not created: %v", err)
	}
}

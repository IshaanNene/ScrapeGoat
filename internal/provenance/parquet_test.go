package provenance

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestParquetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.parquet")

	one := 1
	in := []Record{
		func() Record {
			r := sampleRecord("https://a/")
			r.Text = "Some extracted prose."
			r.Title = "A"
			r.CanonicalURL = "https://a/canonical"
			r.ExtractionConfidence = 0.75
			r.Signals.Licence = "https://creativecommons.org/licenses/by/4.0/"
			r.Signals.LicenceSource = "link"
			r.AIDirectives = SummariseDirectives(ParseRobots("User-agent: GPTBot\nDisallow: /\n"))
			return r
		}(),
		func() Record {
			r := sampleRecord("https://b/")
			r.Signals.NoAI = true
			r.Signals.TDMReservation = &one
			r.Signals.TDMPolicy = "https://b/tdm"
			return r
		}(),
		sampleRecord("https://c/"),
	}

	w, err := NewParquetWriter(path)
	if err != nil {
		t.Fatalf("NewParquetWriter: %v", err)
	}
	for _, r := range in {
		if err := w.Write(r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadParquetCorpus(path)
	if err != nil {
		t.Fatalf("ReadParquetCorpus: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d records, want 3", len(got))
	}

	a := got[0]
	if a.URL != "https://a/" || a.ContentHash != in[0].ContentHash {
		t.Errorf("identity did not survive: %+v", a)
	}
	if a.Text != "Some extracted prose." || a.Title != "A" {
		t.Errorf("content did not survive: %q / %q", a.Text, a.Title)
	}
	if a.CanonicalURL != "https://a/canonical" {
		t.Errorf("canonical = %q", a.CanonicalURL)
	}
	if a.ExtractionConfidence != 0.75 {
		t.Errorf("confidence = %v", a.ExtractionConfidence)
	}
	if a.Signals.Licence == "" || a.Signals.LicenceSource != "link" {
		t.Errorf("licence did not survive: %q / %q", a.Signals.Licence, a.Signals.LicenceSource)
	}
	if a.AIDirectives == nil || len(a.AIDirectives.AgentsBlocked) != 1 {
		t.Errorf("AI directives did not survive: %+v", a.AIDirectives)
	}
	if a.AIDirectives.VendorsBlocked[0] != "OpenAI" {
		t.Errorf("vendors = %v", a.AIDirectives.VendorsBlocked)
	}
	if !a.FetchedAt.Equal(in[0].FetchedAt) {
		t.Errorf("timestamp did not survive: %v vs %v", a.FetchedAt, in[0].FetchedAt)
	}

	b := got[1]
	if !b.Signals.NoAI {
		t.Error("noai did not survive")
	}
	if b.Signals.TDMReservation == nil || *b.Signals.TDMReservation != 1 {
		t.Errorf("TDM reservation = %v, want 1", b.Signals.TDMReservation)
	}
	if b.Signals.TDMPolicy != "https://b/tdm" {
		t.Errorf("TDM policy = %q", b.Signals.TDMPolicy)
	}
}

// The distinction the whole schema is built around, at the point it is easiest to
// lose: a non-nullable column would turn "the page said nothing" into "the page
// said 0", which is permission the source never gave — and it would happen on the
// way to disk, where nobody would see it.
func TestParquetKeepsSilenceDistinctFromZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.parquet")

	zero := 0
	silent := sampleRecord("https://silent/")
	explicit := sampleRecord("https://explicit/")
	explicit.Signals.TDMReservation = &zero

	w, _ := NewParquetWriter(path)
	if err := w.Write(silent); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Write(explicit); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadParquetCorpus(path)
	if err != nil {
		t.Fatalf("ReadParquetCorpus: %v", err)
	}

	if got[0].Signals.TDMReservation != nil {
		t.Errorf("silence became %d through parquet", *got[0].Signals.TDMReservation)
	}
	if got[1].Signals.TDMReservation == nil {
		t.Fatal("an explicit 0 was lost through parquet")
	}
	if *got[1].Signals.TDMReservation != 0 {
		t.Errorf("explicit reservation = %d, want 0", *got[1].Signals.TDMReservation)
	}
}

// Same for robots: no robots.txt at all must not read as one that imposed nothing.
func TestParquetKeepsAbsentRobotsDistinct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.parquet")

	absent := sampleRecord("https://absent/")
	empty := sampleRecord("https://empty/")
	empty.AIDirectives = SummariseDirectives(ParseRobots(""))

	w, _ := NewParquetWriter(path)
	w.Write(absent)
	w.Write(empty)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, _ := ReadParquetCorpus(path)

	if got[0].AIDirectives != nil {
		t.Errorf("a record with no robots.txt gained directives: %+v", got[0].AIDirectives)
	}
	if got[1].AIDirectives == nil {
		t.Fatal("an empty-but-served robots.txt lost its record")
	}
	if !got[1].AIDirectives.RobotsPresent {
		t.Error("RobotsPresent did not survive")
	}
}

// Both formats must agree about what belongs in a corpus. If they diverged, the
// same crawl would produce different datasets depending on a file extension.
func TestParquetAndJSONLAgreeOnCompleteness(t *testing.T) {
	dir := t.TempDir()

	incomplete := Record{URL: "https://bad/"} // no hash, no fetch time
	good := sampleRecord("https://good/")

	pw, _ := NewParquetWriter(filepath.Join(dir, "c.parquet"))
	jw, _ := NewCorpusWriter(filepath.Join(dir, "c.jsonl"))

	for _, w := range []RecordWriter{pw, jw} {
		if err := w.Write(good); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := w.Write(incomplete); err != nil {
			t.Fatalf("Write of an incomplete record should not error: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	pWritten, pSkipped := pw.Stats()
	jWritten, jSkipped := jw.Stats()

	if pWritten != jWritten || pSkipped != jSkipped {
		t.Errorf("formats disagree: parquet %d/%d, jsonl %d/%d",
			pWritten, pSkipped, jWritten, jSkipped)
	}
	if pWritten != 1 || pSkipped != 1 {
		t.Errorf("parquet wrote %d skipped %d, want 1 and 1", pWritten, pSkipped)
	}
}

// The two formats must produce the same records, or the corpus means something
// different depending on how it was written.
func TestParquetAndJSONLProduceTheSameRecords(t *testing.T) {
	dir := t.TempDir()
	one := 1

	r := sampleRecord("https://example.com/page")
	r.Text = "Prose."
	r.Signals.NoAI = true
	r.Signals.TDMReservation = &one
	r.Signals.Licence = "https://example.com/l"
	r.Signals.LicenceSource = "link"
	r.AIDirectives = SummariseDirectives(ParseRobots("User-agent: CCBot\nDisallow: /\n"))

	pPath := filepath.Join(dir, "c.parquet")
	jPath := filepath.Join(dir, "c.jsonl")

	pw, _ := NewParquetWriter(pPath)
	pw.Write(r)
	if err := pw.Close(); err != nil {
		t.Fatalf("parquet Close: %v", err)
	}
	jw, _ := NewCorpusWriter(jPath)
	jw.Write(r)
	jw.Close()

	fromParq, err := ReadAnyCorpus(pPath)
	if err != nil {
		t.Fatalf("read parquet: %v", err)
	}
	fromJSON, err := ReadAnyCorpus(jPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}

	p, j := fromParq[0], fromJSON[0]

	if p.URL != j.URL || p.ContentHash != j.ContentHash || p.Text != j.Text {
		t.Errorf("identity or content differs:\n parquet %+v\n jsonl   %+v", p, j)
	}
	if p.Signals.NoAI != j.Signals.NoAI {
		t.Error("noai differs between formats")
	}
	if (p.Signals.TDMReservation == nil) != (j.Signals.TDMReservation == nil) {
		t.Error("TDM presence differs between formats")
	}
	if p.Signals.TDMReservation != nil && *p.Signals.TDMReservation != *j.Signals.TDMReservation {
		t.Error("TDM value differs between formats")
	}
	if p.Restrictive() != j.Restrictive() {
		t.Error("restrictiveness differs between formats")
	}
	if (p.AIDirectives == nil) != (j.AIDirectives == nil) {
		t.Error("directive presence differs between formats")
	}
	if !p.FetchedAt.Equal(j.FetchedAt) {
		t.Errorf("timestamps differ: %v vs %v", p.FetchedAt, j.FetchedAt)
	}
}

func TestOpenCorpusPicksByExtension(t *testing.T) {
	dir := t.TempDir()

	cases := map[string]string{
		"c.parquet": "*provenance.ParquetWriter",
		"c.pq":      "*provenance.ParquetWriter",
		"c.jsonl":   "*provenance.CorpusWriter",
		"c.json":    "*provenance.CorpusWriter",
		"c":         "*provenance.CorpusWriter",
	}

	for name, want := range cases {
		w, err := OpenCorpus(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("OpenCorpus(%s): %v", name, err)
		}
		if got := typeName(w); got != want {
			t.Errorf("OpenCorpus(%s) = %s, want %s", name, got, want)
		}
		w.Close()
	}
}

func typeName(v any) string {
	switch v.(type) {
	case *ParquetWriter:
		return "*provenance.ParquetWriter"
	case *CorpusWriter:
		return "*provenance.CorpusWriter"
	}
	return "unknown"
}

// A crawl is concurrent, so the writer is too.
func TestParquetConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.parquet")

	w, err := NewParquetWriter(path)
	if err != nil {
		t.Fatalf("NewParquetWriter: %v", err)
	}

	const workers, each = 8, 25
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

	got, err := ReadParquetCorpus(path)
	if err != nil {
		t.Fatalf("ReadParquetCorpus: %v", err)
	}
	if len(got) != workers*each {
		t.Errorf("read %d records, want %d", len(got), workers*each)
	}
}

// More records than fit in one row group, so the multi-row-group path is
// exercised rather than assumed.
func TestParquetSpansRowGroups(t *testing.T) {
	if testing.Short() {
		t.Skip("writes rowGroupSize+ records")
	}
	path := filepath.Join(t.TempDir(), "corpus.parquet")

	w, err := NewParquetWriter(path)
	if err != nil {
		t.Fatalf("NewParquetWriter: %v", err)
	}
	const n = rowGroupSize + 500
	for i := 0; i < n; i++ {
		if err := w.Write(sampleRecord("https://example.com/" + itoa(i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := ReadParquetCorpus(path)
	if err != nil {
		t.Fatalf("ReadParquetCorpus: %v", err)
	}
	if len(got) != n {
		t.Errorf("read %d records across row groups, want %d", len(got), n)
	}
}

// A Parquet file without its footer is not a short corpus, it is not a corpus.
// Close must be the thing that makes it readable, and must say so if it fails.
func TestParquetIsUnreadableUntilClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.parquet")

	w, _ := NewParquetWriter(path)
	for i := 0; i < 5; i++ {
		w.Write(sampleRecord("https://example.com/" + itoa(i)))
	}

	if _, err := ReadParquetCorpus(path); err == nil {
		t.Error("an unclosed parquet file read successfully; the footer cannot have been required")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := ReadParquetCorpus(path)
	if err != nil {
		t.Fatalf("read after close: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("read %d records, want 5", len(got))
	}
}

func TestParquetWriteAfterCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.parquet")

	w, _ := NewParquetWriter(path)
	w.Write(sampleRecord("https://a/"))
	w.Close()

	if err := w.Write(sampleRecord("https://b/")); err == nil {
		t.Error("writing to a closed parquet corpus succeeded")
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestParquetCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "c.parquet")

	w, err := NewParquetWriter(path)
	if err != nil {
		t.Fatalf("NewParquetWriter: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent directory was not created: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

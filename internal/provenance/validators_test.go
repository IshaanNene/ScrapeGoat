package provenance

import (
	"net/http"
	"testing"
	"time"
)

// TestBuildCapturesCacheValidators pins that a record remembers what the server
// said about caching, which is the prerequisite for ever asking it again.
func TestBuildCapturesCacheValidators(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "text/html; charset=utf-8")
	headers.Set("ETag", `W/"686897696a7c876b7e"`)
	headers.Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")

	rec := Build(Source{
		URL:        "https://example.com/a",
		Body:       []byte("<html><body><p>hi</p></body></html>"),
		StatusCode: 200,
		Headers:    headers,
		FetchedAt:  time.Now(),
	}, nil, Content{})

	// Verbatim, weak-comparison marker included. If-None-Match echoes the ETag
	// exactly as received; normalising it here would send the server a value it
	// never issued.
	if rec.ETag != `W/"686897696a7c876b7e"` {
		t.Errorf("ETag = %q, want it stored verbatim", rec.ETag)
	}
	if rec.LastModified != "Wed, 21 Oct 2015 07:28:00 GMT" {
		t.Errorf("LastModified = %q, want the server's own formatting", rec.LastModified)
	}
}

// TestBuildOmitsAbsentValidators keeps "the server offered nothing" distinct from
// "it offered an empty string". A page with no validators is one that will always
// cost a full fetch, and a corpus should be able to count those.
func TestBuildOmitsAbsentValidators(t *testing.T) {
	rec := Build(Source{
		URL:        "https://example.com/a",
		Body:       []byte("<html></html>"),
		StatusCode: 200,
		Headers:    http.Header{},
		FetchedAt:  time.Now(),
	}, nil, Content{})

	if rec.ETag != "" || rec.LastModified != "" {
		t.Errorf("absent validators became %q / %q, want empty", rec.ETag, rec.LastModified)
	}
}

// TestObservationCarriesValidators checks the structured view agrees with the
// record, since the two describe one fetch and a reader may hold either.
func TestObservationCarriesValidators(t *testing.T) {
	rec := Record{
		ContentHash:  "abc",
		URL:          "https://example.com/a",
		ETag:         `"v1"`,
		LastModified: "Wed, 21 Oct 2015 07:28:00 GMT",
	}
	obs := rec.Observation()

	if obs.ETag != rec.ETag {
		t.Errorf("Observation.ETag = %q, record has %q", obs.ETag, rec.ETag)
	}
	if obs.LastModified != rec.LastModified {
		t.Errorf("Observation.LastModified = %q, record has %q", obs.LastModified, rec.LastModified)
	}
}

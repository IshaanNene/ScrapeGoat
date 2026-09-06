package provenance

import (
	"net/http"
	"testing"
	"time"
)

// header builds an http.Header from alternating key/value pairs.
func header(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestRecordFreshness(t *testing.T) {
	fetched := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		cacheControl string
		age          string
		at           time.Duration // from fetched
		wantFresh    bool
	}{
		{"within max-age", "max-age=3600", "", 30 * time.Minute, true},
		{"past max-age", "max-age=3600", "", 90 * time.Minute, false},
		{"exactly at the boundary", "max-age=3600", "", 60 * time.Minute, false},

		// A response relayed by a cache arrived already partly used up. Ignoring
		// Age would treat one that spent most of its life in a CDN as new.
		{"age eats most of the life", "max-age=3600", "3000", 30 * time.Minute, false},
		{"age leaves some life", "max-age=3600", "600", 30 * time.Minute, true},
		{"age exceeds max-age", "max-age=60", "600", time.Second, false},

		// no-cache does not mean "do not store", it means "do not reuse without
		// revalidating" — exactly the decision this makes.
		{"no-cache defeats max-age", "max-age=3600, no-cache", "", time.Minute, false},
		{"no-store defeats max-age", "no-store, max-age=3600", "", time.Minute, false},

		// Field-scoped no-cache applies to named headers, not the body.
		{"field-scoped no-cache is permissive", `max-age=3600, no-cache="Set-Cookie"`, "", time.Minute, true},

		// Nothing to go on: ask the server.
		{"no directive at all", "", "", time.Second, false},
		{"public but no max-age", "public", "", time.Second, false},
		{"malformed max-age", "max-age=soon", "", time.Second, false},
		{"negative max-age", "max-age=-5", "", time.Second, false},
		{"zero max-age", "max-age=0", "", time.Second, false},

		// A malformed Age is a server bug, not a licence to guess high. Treating
		// it as zero errs toward making a request.
		{"malformed age ignored", "max-age=3600", "not-a-number", 30 * time.Minute, true},

		{"case insensitive", "MAX-AGE=3600", "", 30 * time.Minute, true},
		{"whitespace tolerated", "  max-age = 3600 ", "", 30 * time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := Record{FetchedAt: fetched, CacheControl: tt.cacheControl, Age: tt.age}
			if got := rec.IsFresh(fetched.Add(tt.at)); got != tt.wantFresh {
				t.Errorf("IsFresh = %v, want %v (Cache-Control %q, Age %q, +%s)",
					got, tt.wantFresh, tt.cacheControl, tt.age, tt.at)
			}
		})
	}
}

// TestFreshnessNeedsAFetchTime guards the degenerate case: without knowing when
// the copy was taken, max-age measures from nothing.
func TestFreshnessNeedsAFetchTime(t *testing.T) {
	rec := Record{CacheControl: "max-age=3600"}
	if rec.IsFresh(time.Now()) {
		t.Error("a record with no FetchedAt was reported fresh")
	}
	if _, ok := rec.FreshUntil(); ok {
		t.Error("FreshUntil claimed a deadline with no fetch time to measure from")
	}
}

// TestFreshUntilReportsTheDeadline covers the value, not just the boolean, since
// anything reporting what a refresh would cost needs the actual moment.
func TestFreshUntilReportsTheDeadline(t *testing.T) {
	fetched := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	rec := Record{FetchedAt: fetched, CacheControl: "max-age=7200", Age: "1800"}

	until, ok := rec.FreshUntil()
	if !ok {
		t.Fatal("FreshUntil reported no deadline for a record with max-age")
	}
	// 2h of life, half an hour already spent upstream.
	if want := fetched.Add(90 * time.Minute); !until.Equal(want) {
		t.Errorf("FreshUntil = %v, want %v", until, want)
	}
}

func TestBuildCapturesCacheDirectives(t *testing.T) {
	h := header("Cache-Control", "public, max-age=600", "Age", "42")
	rec := Build(Source{
		URL: "https://example.com/a", Body: []byte("<html></html>"),
		StatusCode: 200, Headers: h, FetchedAt: time.Now(),
	}, nil, Content{})

	if rec.CacheControl != "public, max-age=600" {
		t.Errorf("CacheControl = %q, want it verbatim", rec.CacheControl)
	}
	if rec.Age != "42" {
		t.Errorf("Age = %q, want 42", rec.Age)
	}
}

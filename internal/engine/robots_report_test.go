package engine

import (
	"reflect"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
)

// TestReportDoesNotFetch is the property the doc comment always claimed and the
// code did not have: reporting reads the cache, it does not go to the network.
//
// The manager is built with no fetcher, so any attempt to fetch would either panic
// or hang. A cache miss must simply produce an empty report.
func TestReportDoesNotFetch(t *testing.T) {
	rm := NewRobotsManager(false, nil, nil)

	got := rm.Report("https://example.com/page")
	if !reflect.DeepEqual(got, provenance.RobotsReport{}) {
		t.Errorf("Report on a cache miss = %+v, want the zero report", got)
	}
}

// TestReportReturnsWhatEnforcementSaw covers the hit: a report has to be the same
// bytes the crawl obeyed, not a second opinion fetched later.
func TestReportReturnsWhatEnforcementSaw(t *testing.T) {
	rm := NewRobotsManager(true, nil, nil)

	want := provenance.RobotsReport{
		Present:           true,
		AIAgentsAddressed: []string{"GPTBot"},
		AIAgentsBlocked:   []string{"GPTBot"},
	}
	rm.mu.Lock()
	rm.cache["https://example.com"] = &robotsData{report: want}
	rm.mu.Unlock()

	if got := rm.Report("https://example.com/some/page"); !reflect.DeepEqual(got, want) {
		t.Errorf("Report = %+v, want %+v", got, want)
	}
}

// TestReportOnUnparseableURL should not reach the cache at all.
func TestReportOnUnparseableURL(t *testing.T) {
	rm := NewRobotsManager(true, nil, nil)
	if got := rm.Report("://not a url"); !reflect.DeepEqual(got, provenance.RobotsReport{}) {
		t.Errorf("Report on a bad URL = %+v, want the zero report", got)
	}
}

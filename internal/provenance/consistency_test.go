// This file is an external test package on purpose. It imports internal/engine to
// cross-check the two robots.txt parsers, and once provenance is wired into the
// crawl path the engine will import provenance — an in-package test would then be
// an import cycle. Declaring provenance_test keeps the check possible either way.
package provenance_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// robotsFetcher serves one robots.txt body and nothing else.
type robotsFetcher struct{ body string }

func (f *robotsFetcher) Fetch(_ context.Context, req *types.Request) (*types.Response, error) {
	httpResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Request:    &http.Request{URL: req.URL},
	}
	return types.NewResponse(req, httpResp, []byte(f.body), 0), nil
}

func (f *robotsFetcher) Close() error { return nil }

// TestReportAgreesWithEnforcement is the guard on parsing robots.txt twice.
//
// internal/engine parses it to answer "may this crawler fetch this URL"; this
// package parses it to answer "what did the site say, to anyone". The second
// question needs what the first correctly discards, so the duplication is
// deliberate — but the two must not disagree about the groups they both see, or
// the corpus would record a permission the crawl did not actually operate under.
func TestReportAgreesWithEnforcement(t *testing.T) {
	cases := []struct {
		name       string
		robots     string
		path       string
		wantAllow  bool
		wantGroups int
		wantAIOnly bool // the AI block must not change what we are allowed
	}{
		{
			name:       "wildcard allows everything",
			robots:     "User-agent: *\nDisallow:\n",
			path:       "/anything",
			wantAllow:  true,
			wantGroups: 1,
		},
		{
			name:       "wildcard blanket disallow",
			robots:     "User-agent: *\nDisallow: /\n",
			path:       "/anything",
			wantAllow:  false,
			wantGroups: 1,
		},
		{
			name:       "wildcard disallows a subtree",
			robots:     "User-agent: *\nDisallow: /admin/\n",
			path:       "/admin/secret",
			wantAllow:  false,
			wantGroups: 1,
		},
		{
			name:       "subtree rule does not leak to siblings",
			robots:     "User-agent: *\nDisallow: /admin/\n",
			path:       "/public/page",
			wantAllow:  true,
			wantGroups: 1,
		},
		{
			// The case that matters most: a site that turns AI crawlers away while
			// leaving everyone else alone. Enforcement must let us through, and the
			// report must still record that the site said no to somebody.
			name:       "AI agents blocked, wildcard free",
			robots:     "User-agent: *\nDisallow:\n\nUser-agent: GPTBot\nDisallow: /\n\nUser-agent: CCBot\nDisallow: /\n",
			path:       "/article",
			wantAllow:  true,
			wantGroups: 3,
			wantAIOnly: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := "http://robots.example.com"

			rm := engine.NewRobotsManager(true, nil, nil)
			rm.SetFetcher(&robotsFetcher{body: tc.robots})

			gotAllow := rm.IsAllowed(context.Background(), srv+tc.path)
			if gotAllow != tc.wantAllow {
				t.Errorf("enforcement says allowed=%v, want %v", gotAllow, tc.wantAllow)
			}

			report := provenance.ParseRobots(tc.robots)

			// Both parsers must see the same groups. This is the part they genuinely
			// share; path matching belongs to enforcement alone, and the report
			// deliberately does not attempt it.
			if len(report.Groups) != tc.wantGroups {
				t.Errorf("report found %d groups, want %d: %+v",
					len(report.Groups), tc.wantGroups, report.Groups)
			}

			// A blanket disallow on the wildcard group implies enforcement refuses.
			// Only that direction holds: `Disallow: /admin/` refuses a path without
			// being blanket, so the converse would be asserting something false.
			var wildcardBlanket bool
			for _, g := range report.Groups {
				for _, a := range g.Agents {
					if a == "*" && g.BlanketDisallow {
						wildcardBlanket = true
					}
				}
			}
			if wildcardBlanket && gotAllow {
				t.Error("report says the wildcard group blocks everything, but enforcement allowed the fetch")
			}

			if tc.wantAIOnly {
				if !report.AddressesAI() {
					t.Error("the report missed that this site addresses AI crawlers")
				}
				if len(report.AIAgentsBlocked) != 2 {
					t.Errorf("AIAgentsBlocked = %v, want two", report.AIAgentsBlocked)
				}
				if !gotAllow {
					t.Error("an AI-only block changed what this crawler is permitted")
				}
			}
		})
	}
}

// A record built from a crawl that was permitted, on a site that blocks AI
// crawlers, must come out permitted *and* restrictive. Those are different
// questions and conflating them is how a corpus ends up indefensible.
func TestPermittedButRestrictive(t *testing.T) {
	robots := "User-agent: *\nDisallow:\n\nUser-agent: GPTBot\nDisallow: /\n"

	rm := engine.NewRobotsManager(true, nil, nil)
	rm.SetFetcher(&robotsFetcher{body: robots})

	url := "http://example.com/article"
	allowed := rm.IsAllowed(context.Background(), url)
	if !allowed {
		t.Fatal("this crawler should be permitted")
	}

	rec := provenance.Record{
		SchemaVersion: provenance.SchemaVersion,
		URL:           url,
		ContentHash:   "abc123",
		FetchedAt:     mustNow(),
		RobotsAllowed: allowed,
		AIDirectives:  provenance.SummariseDirectives(provenance.ParseRobots(robots)),
	}

	if !rec.RobotsAllowed {
		t.Error("record lost the permission the crawl actually had")
	}
	if !rec.Restrictive() {
		t.Error("record does not reflect that the site turned AI crawlers away")
	}
	if !rec.Complete() {
		t.Error("record is not complete")
	}
	if got := rec.AIDirectives.VendorsBlocked; len(got) != 1 || got[0] != "OpenAI" {
		t.Errorf("VendorsBlocked = %v", got)
	}
}

func mustNow() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC) }

package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRobotsManagerIsAllowed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte(`User-agent: *
Disallow: /private/
Disallow: /admin
Allow: /private/public
Crawl-delay: 2
Sitemap: https://example.com/sitemap.xml`))
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	tests := []struct {
		name    string
		path    string
		allowed bool
	}{
		{"root allowed", "/", true},
		{"public page allowed", "/products", true},
		{"private blocked", "/private/secret", false},
		{"private/public allowed (Allow overrides)", "/private/public", true},
		{"admin blocked", "/admin", false},
		{"admin subpath blocked", "/admin/users", false},
		{"unrelated path allowed", "/about", true},
	}

	rm := NewRobotsManager(true)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rm.IsAllowed(ts.URL + tt.path)
			if got != tt.allowed {
				t.Errorf("IsAllowed(%s) = %v, want %v", tt.path, got, tt.allowed)
			}
		})
	}
}

func TestRobotsManagerDisabled(t *testing.T) {
	rm := NewRobotsManager(false)

	tests := []string{
		"https://example.com/private",
		"https://example.com/admin",
		"https://example.com/anything",
	}

	for _, u := range tests {
		if !rm.IsAllowed(u) {
			t.Errorf("should allow %q when disabled", u)
		}
	}
}

func TestRobotsManagerSitemaps(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte(`User-agent: *
Disallow: /private/
Sitemap: https://example.com/sitemap1.xml
Sitemap: https://example.com/sitemap2.xml`))
			return
		}
	}))
	defer ts.Close()

	rm := NewRobotsManager(true)
	// Trigger fetch by calling IsAllowed
	rm.IsAllowed(ts.URL + "/test")

	sitemaps := rm.GetSitemaps(ts.URL)
	if len(sitemaps) != 2 {
		t.Errorf("expected 2 sitemaps, got %d", len(sitemaps))
	}
}

func TestRobotsManager404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer ts.Close()

	rm := NewRobotsManager(true)
	// If robots.txt returns 404, all URLs should be allowed
	if !rm.IsAllowed(ts.URL + "/anything") {
		t.Error("should allow all when robots.txt returns 404")
	}
}

func TestMatchRobotsPattern(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/", "/anything", true},
		{"/admin", "/admin", true},
		{"/admin", "/admin/users", true},
		{"/admin", "/public", false},
		{"/*.pdf$", "/doc.pdf", true},
		{"/*.pdf$", "/doc.pdf?v=1", false},
		{"/*/secret", "/foo/secret", true},
		{"", "/anything", false},
		{"/api/*", "/api/v1/users", true},
		{"/api/*", "/other", false},
	}

	for _, tt := range tests {
		name := tt.pattern + "→" + tt.path
		t.Run(name, func(t *testing.T) {
			got := matchRobotsPattern(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchRobotsPattern(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestParseRobotsTxt(t *testing.T) {
	content := `User-agent: *
Disallow: /private/
Allow: /private/public
Crawl-delay: 3
Sitemap: https://example.com/sitemap.xml

User-agent: BadBot
Disallow: /`

	data := parseRobotsTxt(content)
	if data == nil {
		t.Fatal("parseRobotsTxt returned nil")
	}
	if len(data.disallowed) != 1 || data.disallowed[0] != "/private/" {
		t.Errorf("disallowed = %v, want [/private/]", data.disallowed)
	}
	if len(data.allowed) != 1 || data.allowed[0] != "/private/public" {
		t.Errorf("allowed = %v, want [/private/public]", data.allowed)
	}
	if data.crawlDelay != 3_000_000_000 { // 3 seconds in nanoseconds
		t.Errorf("crawlDelay = %v, want 3s", data.crawlDelay)
	}
	if len(data.sitemaps) != 1 {
		t.Errorf("sitemaps count = %d, want 1", len(data.sitemaps))
	}
}

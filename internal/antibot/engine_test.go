package antibot

import (
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func makeResponse(statusCode int, headers map[string][]string, body string) *types.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &types.Response{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       []byte(body),
	}
}

func TestBlockDetector_NotBlocked(t *testing.T) {
	d := NewBlockDetector(testLogger())
	resp := makeResponse(200, nil, "<html><body>Normal page</body></html>")
	result := d.Detect(resp)
	if result.Blocked {
		t.Error("normal 200 should not be blocked")
	}
}

func TestBlockDetector_Cloudflare(t *testing.T) {
	d := NewBlockDetector(testLogger())
	headers := http.Header{
		"Cf-Ray": []string{"abc123"},
		"Server": []string{"cloudflare"},
	}
	resp := makeResponse(403, headers, "<html>cf-browser-verification challenge</html>")
	result := d.Detect(resp)
	if !result.Blocked {
		t.Errorf("Cloudflare challenge should be detected as blocked, score=%f", result.Score)
	}
	if result.Reason != Cloudflare {
		t.Errorf("reason = %s, want cloudflare", result.Reason)
	}
}

func TestBlockDetector_Captcha(t *testing.T) {
	d := NewBlockDetector(testLogger())
	resp := makeResponse(200, nil, `<html><div class="g-recaptcha" data-sitekey="abc"></div></html>`)
	result := d.Detect(resp)
	if !result.Blocked {
		t.Errorf("CAPTCHA page should be detected as blocked, score=%f", result.Score)
	}
	if result.Reason != Captcha {
		t.Errorf("reason = %s, want captcha", result.Reason)
	}
}

func TestBlockDetector_RateLimit(t *testing.T) {
	d := NewBlockDetector(testLogger())
	headers := http.Header{"Retry-After": []string{"60"}}
	resp := makeResponse(429, headers, "Rate limited")
	result := d.Detect(resp)
	if !result.Blocked {
		t.Errorf("429 should be detected as blocked, score=%f", result.Score)
	}
	if result.Reason != RateLimit {
		t.Errorf("reason = %s, want rate_limit", result.Reason)
	}
}

func TestBlockDetector_DataDome(t *testing.T) {
	d := NewBlockDetector(testLogger())
	headers := http.Header{"X-Datadome": []string{"protected"}}
	resp := makeResponse(403, headers, "datadome challenge page")
	result := d.Detect(resp)
	if !result.Blocked {
		t.Errorf("DataDome should be detected, score=%f", result.Score)
	}
}

func TestBlockDetector_CustomPattern(t *testing.T) {
	d := NewBlockDetector(testLogger())
	d.RegisterPattern(DetectionPattern{
		Name:        "custom_waf",
		Reason:      ContentBlocked,
		StatusCodes: []int{403},
		BodyPattern: mustCompile(`custom-waf-block-page`),
		Weight:      0.9,
	})

	resp := makeResponse(403, nil, "custom-waf-block-page detected bad request")
	result := d.Detect(resp)
	if !result.Blocked {
		t.Errorf("custom pattern should be detected, score=%f", result.Score)
	}
}

func TestAdaptiveStrategy_Escalation(t *testing.T) {
	d := NewBlockDetector(testLogger())
	s := NewAdaptiveStrategy(d, testLogger())

	// Normal responses should continue.
	normalResp := makeResponse(200, nil, "OK")
	action := s.RecordResponse("example.com", normalResp)
	if action.Type != ActionContinue {
		t.Errorf("normal response should continue, got %d", action.Type)
	}

	// Rate limit should slow down.
	rlHeaders := http.Header{"Retry-After": []string{"30"}}
	rlResp := makeResponse(429, rlHeaders, "Too Many Requests")
	action = s.RecordResponse("example.com", rlResp)
	if action.Type != ActionSlowDown {
		t.Errorf("rate limit should slow down, got %d", action.Type)
	}
}

func TestAdaptiveStrategy_DomainPause(t *testing.T) {
	d := NewBlockDetector(testLogger())
	s := NewAdaptiveStrategy(d, testLogger())

	// Flood with blocks to trigger domain pause.
	cfHeaders := http.Header{"Cf-Ray": []string{"x"}, "Server": []string{"cloudflare"}}
	for i := 0; i < 10; i++ {
		resp := makeResponse(403, cfHeaders, "cf-browser-verification")
		s.RecordResponse("blocked.com", resp)
	}

	stats := s.GetDomainStats("blocked.com")
	if stats == nil {
		t.Fatal("expected domain stats")
	}
	blockRate := float64(stats.Blocked) / float64(stats.Total)
	if blockRate < 0.5 {
		t.Errorf("block rate = %f, expected >0.5", blockRate)
	}
}

func TestHumanBehaviour_MousePath(t *testing.T) {
	h := NewHumanBehaviour()
	path := h.MousePath(0, 0, 500, 300)
	if len(path) < 5 {
		t.Errorf("path should have at least 5 points, got %d", len(path))
	}
	// First point should be near origin.
	if path[0][0] > 10 || path[0][1] > 10 {
		t.Errorf("first point too far from origin: %v", path[0])
	}
}

func TestHumanBehaviour_TypingSpeed(t *testing.T) {
	h := NewHumanBehaviour()
	delays := h.TypingSpeed("hello world")
	if len(delays) != 11 {
		t.Errorf("delay count = %d, want 11", len(delays))
	}
	for i, d := range delays {
		if d < 0 {
			t.Errorf("delay %d is negative: %v", i, d)
		}
	}
}

func TestStealthProfiles(t *testing.T) {
	profiles := DefaultProfiles()
	if len(profiles) != 5 {
		t.Errorf("profile count = %d, want 5", len(profiles))
	}
	for _, p := range profiles {
		if p.UserAgent == "" {
			t.Errorf("profile %s has empty user agent", p.Name)
		}
		if p.Navigator.Webdriver {
			t.Errorf("profile %s has webdriver=true (will be detected)", p.Name)
		}
		if p.Screen.Width == 0 {
			t.Errorf("profile %s has zero screen width", p.Name)
		}
	}
}

func TestRandomProfile(t *testing.T) {
	p := RandomProfile()
	if p.Name == "" {
		t.Error("random profile should have a name")
	}
}

func TestBlockReason_String(t *testing.T) {
	tests := []struct {
		reason BlockReason
		want   string
	}{
		{NotBlocked, "not_blocked"},
		{Cloudflare, "cloudflare"},
		{DataDome, "datadome"},
		{PerimeterX, "perimeterx"},
	}
	for _, tt := range tests {
		if got := tt.reason.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}


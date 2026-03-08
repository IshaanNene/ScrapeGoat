package fetcher

import (
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
)

// RequestFingerprinter diversifies request fingerprints to avoid bot detection.
// Modern anti-bot systems analyze request header ordering, TLS fingerprints,
// and behavioral patterns. This component randomizes these signals.
type RequestFingerprinter struct {
	logger   *slog.Logger
	profiles []BrowserProfile
}

// BrowserProfile represents a browser's typical request fingerprint.
type BrowserProfile struct {
	Name          string
	HeaderOrder   []string
	Accept        string
	AcceptLang    string
	SecChUa       string
	SecChPlatform string
	Platform      string
}

// NewRequestFingerprinter creates a fingerprinter with built-in browser profiles.
func NewRequestFingerprinter(logger *slog.Logger) *RequestFingerprinter {
	return &RequestFingerprinter{
		logger: logger.With("component", "fingerprinter"),
		profiles: []BrowserProfile{
			{
				Name:          "chrome-windows",
				HeaderOrder:   []string{"Host", "Connection", "Cache-Control", "Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform", "Upgrade-Insecure-Requests", "User-Agent", "Accept", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-User", "Sec-Fetch-Dest", "Accept-Encoding", "Accept-Language"},
				Accept:        "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
				AcceptLang:    "en-US,en;q=0.9",
				SecChUa:       `"Chromium";v="120", "Not?A_Brand";v="8", "Google Chrome";v="120"`,
				SecChPlatform: `"Windows"`,
				Platform:      "Win32",
			},
			{
				Name:          "chrome-mac",
				HeaderOrder:   []string{"Host", "Connection", "Cache-Control", "Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform", "Upgrade-Insecure-Requests", "User-Agent", "Accept", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-User", "Sec-Fetch-Dest", "Accept-Encoding", "Accept-Language"},
				Accept:        "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
				AcceptLang:    "en-US,en;q=0.9",
				SecChUa:       `"Chromium";v="121", "Not A(Brand";v="99", "Google Chrome";v="121"`,
				SecChPlatform: `"macOS"`,
				Platform:      "MacIntel",
			},
			{
				Name:          "firefox-windows",
				HeaderOrder:   []string{"Host", "User-Agent", "Accept", "Accept-Language", "Accept-Encoding", "Connection", "Upgrade-Insecure-Requests", "Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-User"},
				Accept:        "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
				AcceptLang:    "en-US,en;q=0.5",
				SecChUa:       "",
				SecChPlatform: "",
				Platform:      "Win32",
			},
			{
				Name:          "firefox-linux",
				HeaderOrder:   []string{"Host", "User-Agent", "Accept", "Accept-Language", "Accept-Encoding", "Connection", "Upgrade-Insecure-Requests", "Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-User"},
				Accept:        "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
				AcceptLang:    "en-US,en;q=0.5",
				SecChUa:       "",
				SecChPlatform: "",
				Platform:      "Linux x86_64",
			},
			{
				Name:          "safari-mac",
				HeaderOrder:   []string{"Host", "Accept", "User-Agent", "Accept-Language", "Accept-Encoding", "Connection"},
				Accept:        "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
				AcceptLang:    "en-US,en;q=0.9",
				SecChUa:       "",
				SecChPlatform: "",
				Platform:      "MacIntel",
			},
			{
				Name:          "edge-windows",
				HeaderOrder:   []string{"Host", "Connection", "Cache-Control", "Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform", "Upgrade-Insecure-Requests", "User-Agent", "Accept", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-User", "Sec-Fetch-Dest", "Accept-Encoding", "Accept-Language"},
				Accept:        "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
				AcceptLang:    "en-US,en;q=0.9",
				SecChUa:       `"Not_A Brand";v="8", "Chromium";v="120", "Microsoft Edge";v="120"`,
				SecChPlatform: `"Windows"`,
				Platform:      "Win32",
			},
		},
	}
}

// RandomProfile returns a random browser profile.
func (rf *RequestFingerprinter) RandomProfile() BrowserProfile {
	return rf.profiles[rand.Intn(len(rf.profiles))]
}

// ApplyProfile applies a browser profile to an HTTP request.
func (rf *RequestFingerprinter) ApplyProfile(req *http.Request, profile BrowserProfile) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", profile.Accept)
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", profile.AcceptLang)
	}
	if profile.SecChUa != "" && req.Header.Get("Sec-Ch-Ua") == "" {
		req.Header.Set("Sec-Ch-Ua", profile.SecChUa)
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Platform", profile.SecChPlatform)
	}
	if req.Header.Get("Sec-Fetch-Dest") == "" {
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Site", "none")
		req.Header.Set("Sec-Fetch-User", "?1")
	}
}

// Profiles returns all available browser profiles.
func (rf *RequestFingerprinter) Profiles() []string {
	names := make([]string, len(rf.profiles))
	for i, p := range rf.profiles {
		names[i] = p.Name
	}
	return names
}

// IsCloudflareChallenge checks response for Cloudflare challenge indicators.
func IsCloudflareChallenge(statusCode int, body []byte, headers http.Header) bool {
	if statusCode != 403 && statusCode != 503 {
		return false
	}

	// Check headers
	if headers.Get("Cf-Ray") != "" || headers.Get("Server") == "cloudflare" {
		bodyStr := strings.ToLower(string(body))
		cfMarkers := []string{
			"cloudflare",
			"cf-browser-verification",
			"jschl-answer",
			"cf_chl_opt",
			"challenge-platform",
		}
		for _, marker := range cfMarkers {
			if strings.Contains(bodyStr, marker) {
				return true
			}
		}
	}

	return false
}

// IsRateLimited checks if a response indicates rate limiting.
func IsRateLimited(statusCode int, headers http.Header) bool {
	if statusCode == 429 {
		return true
	}
	// Some sites use 403 with rate limit headers
	if statusCode == 403 {
		if headers.Get("X-RateLimit-Remaining") == "0" {
			return true
		}
		if headers.Get("Retry-After") != "" {
			return true
		}
	}
	return false
}

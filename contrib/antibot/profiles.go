package antibot

import (
	"math/rand"
)

// BrowserStealthProfile contains a complete browser fingerprint for stealth crawling.
type BrowserStealthProfile struct {
	Name      string            `json:"name"`
	UserAgent string            `json:"user_agent"`
	Platform  string            `json:"platform"`
	Vendor    string            `json:"vendor"`
	Language  string            `json:"language"`
	Languages []string          `json:"languages"`
	Screen    ScreenProfile     `json:"screen"`
	Navigator NavigatorProfile  `json:"navigator"`
	WebGL     WebGLProfile      `json:"webgl"`
	Headers   map[string]string `json:"headers"`
}

// ScreenProfile emulates a screen configuration.
type ScreenProfile struct {
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	AvailWidth  int     `json:"avail_width"`
	AvailHeight int     `json:"avail_height"`
	ColorDepth  int     `json:"color_depth"`
	PixelRatio  float64 `json:"pixel_ratio"`
}

// NavigatorProfile emulates navigator properties.
type NavigatorProfile struct {
	MaxTouchPoints      int    `json:"max_touch_points"`
	HardwareConcurrency int    `json:"hardware_concurrency"`
	DeviceMemory        int    `json:"device_memory"`
	DoNotTrack          string `json:"do_not_track"`
	CookieEnabled       bool   `json:"cookie_enabled"`
	PDFViewerEnabled    bool   `json:"pdf_viewer_enabled"`
	Webdriver           bool   `json:"webdriver"` // Must be false to avoid detection.
}

// WebGLProfile emulates WebGL renderer info.
type WebGLProfile struct {
	Vendor   string `json:"vendor"`
	Renderer string `json:"renderer"`
}

// DefaultProfiles returns the built-in stealth profiles.
func DefaultProfiles() []BrowserStealthProfile {
	return []BrowserStealthProfile{
		chromeWindows(),
		chromeMac(),
		firefoxLinux(),
		safariMac(),
		edgeWindows(),
	}
}

// RandomProfile returns a random stealth profile.
func RandomProfile() BrowserStealthProfile {
	profiles := DefaultProfiles()
	return profiles[rand.Intn(len(profiles))]
}

func chromeWindows() BrowserStealthProfile {
	return BrowserStealthProfile{
		Name:      "chrome_windows",
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
		Platform:  "Win32",
		Vendor:    "Google Inc.",
		Language:  "en-US",
		Languages: []string{"en-US", "en"},
		Screen:    ScreenProfile{Width: 1920, Height: 1080, AvailWidth: 1920, AvailHeight: 1040, ColorDepth: 24, PixelRatio: 1.0},
		Navigator: NavigatorProfile{MaxTouchPoints: 0, HardwareConcurrency: 8, DeviceMemory: 8, DoNotTrack: "unspecified", CookieEnabled: true, PDFViewerEnabled: true, Webdriver: false},
		WebGL:     WebGLProfile{Vendor: "Google Inc. (NVIDIA)", Renderer: "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		Headers: map[string]string{
			"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
			"Accept-Language":           "en-US,en;q=0.9",
			"Accept-Encoding":           "gzip, deflate, br",
			"Sec-Ch-Ua":                 `"Google Chrome";v="125", "Chromium";v="125", "Not.A/Brand";v="24"`,
			"Sec-Ch-Ua-Mobile":          "?0",
			"Sec-Ch-Ua-Platform":        `"Windows"`,
			"Sec-Fetch-Dest":            "document",
			"Sec-Fetch-Mode":            "navigate",
			"Sec-Fetch-Site":            "none",
			"Sec-Fetch-User":            "?1",
			"Upgrade-Insecure-Requests": "1",
		},
	}
}

func chromeMac() BrowserStealthProfile {
	return BrowserStealthProfile{
		Name:      "chrome_mac",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
		Platform:  "MacIntel",
		Vendor:    "Google Inc.",
		Language:  "en-US",
		Languages: []string{"en-US", "en"},
		Screen:    ScreenProfile{Width: 2560, Height: 1600, AvailWidth: 2560, AvailHeight: 1575, ColorDepth: 30, PixelRatio: 2.0},
		Navigator: NavigatorProfile{MaxTouchPoints: 0, HardwareConcurrency: 10, DeviceMemory: 8, DoNotTrack: "unspecified", CookieEnabled: true, PDFViewerEnabled: true, Webdriver: false},
		WebGL:     WebGLProfile{Vendor: "Google Inc. (Apple)", Renderer: "ANGLE (Apple, Apple M1 Pro, OpenGL 4.1)"},
		Headers: map[string]string{
			"Accept":             "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
			"Accept-Language":    "en-US,en;q=0.9",
			"Sec-Ch-Ua":          `"Google Chrome";v="125", "Chromium";v="125", "Not.A/Brand";v="24"`,
			"Sec-Ch-Ua-Mobile":   "?0",
			"Sec-Ch-Ua-Platform": `"macOS"`,
		},
	}
}

func firefoxLinux() BrowserStealthProfile {
	return BrowserStealthProfile{
		Name:      "firefox_linux",
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0",
		Platform:  "Linux x86_64",
		Vendor:    "",
		Language:  "en-US",
		Languages: []string{"en-US", "en"},
		Screen:    ScreenProfile{Width: 1920, Height: 1080, AvailWidth: 1920, AvailHeight: 1053, ColorDepth: 24, PixelRatio: 1.0},
		Navigator: NavigatorProfile{MaxTouchPoints: 0, HardwareConcurrency: 12, DeviceMemory: 0, DoNotTrack: "unspecified", CookieEnabled: true, PDFViewerEnabled: true, Webdriver: false},
		WebGL:     WebGLProfile{Vendor: "Mesa", Renderer: "Mesa Intel(R) UHD Graphics 630 (CFL GT2)"},
		Headers: map[string]string{
			"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
			"Accept-Language": "en-US,en;q=0.5",
			"Accept-Encoding": "gzip, deflate, br",
		},
	}
}

func safariMac() BrowserStealthProfile {
	return BrowserStealthProfile{
		Name:      "safari_mac",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
		Platform:  "MacIntel",
		Vendor:    "Apple Computer, Inc.",
		Language:  "en-US",
		Languages: []string{"en-US"},
		Screen:    ScreenProfile{Width: 2560, Height: 1440, AvailWidth: 2560, AvailHeight: 1415, ColorDepth: 30, PixelRatio: 2.0},
		Navigator: NavigatorProfile{MaxTouchPoints: 0, HardwareConcurrency: 8, DeviceMemory: 0, DoNotTrack: "unspecified", CookieEnabled: true, PDFViewerEnabled: true, Webdriver: false},
		WebGL:     WebGLProfile{Vendor: "Apple Inc.", Renderer: "Apple GPU"},
		Headers: map[string]string{
			"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"Accept-Language": "en-US,en;q=0.9",
		},
	}
}

func edgeWindows() BrowserStealthProfile {
	return BrowserStealthProfile{
		Name:      "edge_windows",
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
		Platform:  "Win32",
		Vendor:    "Google Inc.",
		Language:  "en-US",
		Languages: []string{"en-US", "en"},
		Screen:    ScreenProfile{Width: 1920, Height: 1080, AvailWidth: 1920, AvailHeight: 1040, ColorDepth: 24, PixelRatio: 1.25},
		Navigator: NavigatorProfile{MaxTouchPoints: 0, HardwareConcurrency: 8, DeviceMemory: 8, DoNotTrack: "unspecified", CookieEnabled: true, PDFViewerEnabled: true, Webdriver: false},
		WebGL:     WebGLProfile{Vendor: "Google Inc. (Intel)", Renderer: "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		Headers: map[string]string{
			"Accept":             "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8",
			"Accept-Language":    "en-US,en;q=0.9",
			"Sec-Ch-Ua":          `"Microsoft Edge";v="125", "Chromium";v="125", "Not.A/Brand";v="24"`,
			"Sec-Ch-Ua-Mobile":   "?0",
			"Sec-Ch-Ua-Platform": `"Windows"`,
		},
	}
}

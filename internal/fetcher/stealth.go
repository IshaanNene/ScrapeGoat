package fetcher

import (
	"fmt"
	"math/rand"
)

// StealthConfig configures anti-detection and fingerprint spoofing.
type StealthConfig struct {
	// Enable TLS fingerprint randomization
	TLSFingerprint bool

	// Custom viewport dimensions
	ViewportWidth  int
	ViewportHeight int

	// Window size for browser launch
	WindowSize string

	// UserDataDir for persistent browser profile
	UserDataDir string

	// DisableWebGL hides WebGL fingerprinting
	DisableWebGL bool

	// Timezone override (e.g., "America/New_York")
	Timezone string

	// Language override (e.g., "en-US")
	Language string

	// Platform override (e.g., "Win32", "MacIntel", "Linux x86_64")
	Platform string

	// ScreenResolution for canvas fingerprinting defense
	ScreenWidth  int
	ScreenHeight int

	// Hardware concurrency (number of CPU cores to report)
	HardwareConcurrency int

	// DeviceMemory (GB of RAM to report)
	DeviceMemory int

	// WebRTC mode: "disable", "default_public_interface_only", "disable_non_proxied_udp"
	WebRTCMode string
}

// DefaultStealthConfig returns a stealth configuration that mimics a typical desktop browser.
func DefaultStealthConfig() *StealthConfig {
	viewports := []struct{ w, h int }{
		{1920, 1080}, {1366, 768}, {1536, 864},
		{1440, 900}, {1280, 720}, {2560, 1440},
	}
	vp := viewports[rand.Intn(len(viewports))]

	platforms := []string{"Win32", "MacIntel", "Linux x86_64"}
	platform := platforms[rand.Intn(len(platforms))]

	return &StealthConfig{
		TLSFingerprint:      true,
		ViewportWidth:       vp.w,
		ViewportHeight:      vp.h,
		WindowSize:          fmt.Sprintf("%d,%d", vp.w, vp.h),
		DisableWebGL:        false,
		Language:            "en-US",
		Platform:            platform,
		ScreenWidth:         vp.w,
		ScreenHeight:        vp.h,
		HardwareConcurrency: 4 + rand.Intn(13), // 4-16 cores
		DeviceMemory:        8,
		WebRTCMode:          "disable_non_proxied_udp",
	}
}

// StealthJS returns JavaScript to inject for fingerprint spoofing.
// This is injected into every page before any other scripts run.
func (sc *StealthConfig) StealthJS() string {
	return fmt.Sprintf(`
// --- ScrapeGoat Stealth Mode ---

// Override navigator properties
Object.defineProperty(navigator, 'platform', { get: () => '%s' });
Object.defineProperty(navigator, 'language', { get: () => '%s' });
Object.defineProperty(navigator, 'languages', { get: () => ['%s', 'en'] });
Object.defineProperty(navigator, 'hardwareConcurrency', { get: () => %d });
Object.defineProperty(navigator, 'deviceMemory', { get: () => %d });

// Remove webdriver flag
Object.defineProperty(navigator, 'webdriver', { get: () => false });

// Override Chrome properties
window.chrome = {
	runtime: { onMessage: { addListener: () => {} }, sendMessage: () => {} },
	loadTimes: () => ({}),
	csi: () => ({}),
};

// Fix permissions API
const originalQuery = window.navigator.permissions.query;
window.navigator.permissions.query = (parameters) => (
	parameters.name === 'notifications' ?
		Promise.resolve({ state: Notification.permission }) :
		originalQuery(parameters)
);

// Fix plugins array
Object.defineProperty(navigator, 'plugins', {
	get: () => {
		const plugins = [
			{ name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer' },
			{ name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai' },
			{ name: 'Native Client', filename: 'internal-nacl-plugin' },
		];
		plugins.length = 3;
		return plugins;
	}
});

// Fix iframe contentWindow
const iframeProto = HTMLIFrameElement.prototype;
const origContentWindow = Object.getOwnPropertyDescriptor(iframeProto, 'contentWindow');
if (origContentWindow) {
	Object.defineProperty(iframeProto, 'contentWindow', {
		get: function() {
			const win = origContentWindow.get.call(this);
			if (win) {
				try { win.chrome = window.chrome; } catch(e) {}
			}
			return win;
		}
	});
}

// Console debug logging protection
const originalToString = Function.prototype.toString;
Function.prototype.toString = function() {
	if (this === Function.prototype.toString) return 'function toString() { [native code] }';
	return originalToString.call(this);
};
`, sc.Platform, sc.Language, sc.Language, sc.HardwareConcurrency, sc.DeviceMemory)
}

// TLSTransport creates an http.Transport with randomized TLS fingerprint.

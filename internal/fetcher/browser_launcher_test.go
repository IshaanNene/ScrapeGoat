package fetcher

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/launcher/flags"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/safety"
)

// bannedFlags each disable a containment boundary Chromium provides. They were all
// set at one point, in a process that renders pages chosen by whoever the crawler is
// pointed at, which meant a renderer compromise from a crawled page reached the host
// and any page could read cross-origin responses. This test is here so that
// restoring one to fix a startup problem is a deliberate act with a failing test
// attached, rather than a one-line convenience.
var bannedFlags = []flags.Flag{
	"no-sandbox",
	"disable-setuid-sandbox",
	"disable-web-security",
}

func TestLauncherKeepsChromiumSandboxEnabled(t *testing.T) {
	l, err := testFetcher(t).newLauncher()
	if err != nil {
		t.Fatalf("newLauncher: %v", err)
	}

	for _, f := range bannedFlags {
		if l.Has(f) {
			t.Errorf("launcher sets --%s; it disables a security boundary and must stay off", f)
		}
	}

	// disable-features is allowed to exist, but not to name the isolation ones.
	if l.Has("disable-features") {
		vals, _ := l.GetFlags("disable-features")
		got := strings.Join(vals, ",")
		for _, banned := range []string{"IsolateOrigins", "site-per-process"} {
			if strings.Contains(got, banned) {
				t.Errorf("launcher disables %s via --disable-features=%s; site isolation must stay on", banned, got)
			}
		}
	}
}

// TestLauncherRoutesEgressThroughGuardedProxy pins the other half of the fix: the
// flags being safe is worth nothing if Chromium still resolves and dials on its own.
func TestLauncherRoutesEgressThroughGuardedProxy(t *testing.T) {
	bf := testFetcher(t)
	l, err := bf.newLauncher()
	if err != nil {
		t.Fatalf("newLauncher: %v", err)
	}
	t.Cleanup(bf.closeEgress)

	if !l.Has("proxy-server") {
		t.Fatal("launcher sets no --proxy-server; browser egress would bypass the guard entirely")
	}
	if bf.egress == nil {
		t.Fatal("no guarded egress proxy was started")
	}
	proxyVals, _ := l.GetFlags("proxy-server")
	if got := strings.Join(proxyVals, ","); got != bf.egress.Addr() {
		t.Errorf("--proxy-server = %q, want the guarded proxy at %q", got, bf.egress.Addr())
	}

	// Without this, Chromium's implicit bypass rules exempt loopback and link-local
	// from the proxy — the exact destinations the proxy exists to refuse.
	bypassVals, _ := l.GetFlags("proxy-bypass-list")
	if got := strings.Join(bypassVals, ","); got != "<-loopback>" {
		t.Errorf("--proxy-bypass-list = %q, want %q so loopback and link-local are proxied too", got, "<-loopback>")
	}
}

func TestNewBrowserFetcherRequiresGuard(t *testing.T) {
	_, err := NewBrowserFetcher(config.DefaultConfig(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("NewBrowserFetcher with a nil guard returned no error; an unguarded browser must not be constructible")
	}
}

func testFetcher(t *testing.T) *BrowserFetcher {
	t.Helper()
	return &BrowserFetcher{
		cfg:    config.DefaultConfig(),
		guard:  safety.Default(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

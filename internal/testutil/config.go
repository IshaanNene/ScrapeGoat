// Package testutil holds helpers shared across ScrapeGoat's test packages.
// Nothing outside a _test.go file should import it.
package testutil

import "github.com/IshaanNene/ScrapeGoat/internal/config"

// LoopbackConfig returns the default configuration with the outbound address
// checks relaxed, so the fetcher can reach an httptest server.
//
// httptest.NewServer always binds 127.0.0.1, and the default safety policy refuses
// to dial loopback — that refusal is the SSRF guard doing its job, not a bug. Tests
// that drive a local server opt out here, explicitly and in one place, rather than
// the production default being weakened to accommodate them.
//
// Never use this outside tests: it makes the process willing to fetch
// 169.254.169.254 and anything else on the local network.
func LoopbackConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Safety.AllowPrivateAddresses = true
	return cfg
}

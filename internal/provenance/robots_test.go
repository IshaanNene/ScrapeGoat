package provenance

import (
	"strings"
	"testing"
)

func TestParseRobotsGroups(t *testing.T) {
	report := ParseRobots(`
# A typical file
User-agent: *
Disallow: /admin/
Allow: /admin/public/

User-agent: GPTBot
Disallow: /

User-agent: CCBot
User-agent: anthropic-ai
Disallow: /

Sitemap: https://example.com/sitemap.xml
`)

	if !report.Present {
		t.Error("Present is false for a file that was parsed")
	}
	if len(report.Groups) != 3 {
		t.Fatalf("got %d groups, want 3: %+v", len(report.Groups), report.Groups)
	}

	// Consecutive User-agent lines share one group.
	third := report.Groups[2]
	if len(third.Agents) != 2 {
		t.Errorf("consecutive user-agent lines did not share a group: %v", third.Agents)
	}

	if !report.AddressesAI() {
		t.Error("AddressesAI is false for a file naming GPTBot")
	}
	want := []string{"anthropic-ai", "ccbot", "gptbot"}
	if strings.Join(report.AIAgentsBlocked, ",") != strings.Join(want, ",") {
		t.Errorf("blocked = %v, want %v", report.AIAgentsBlocked, want)
	}
	if len(report.Sitemaps) != 1 {
		t.Errorf("sitemaps = %v", report.Sitemaps)
	}
}

// The wildcard group is not an AI directive. A site that disallows everything for
// everyone has not singled out AI crawlers, and reporting it as though it had
// would overstate what the site said.
func TestParseRobotsWildcardIsNotAnAIDirective(t *testing.T) {
	report := ParseRobots("User-agent: *\nDisallow: /\n")

	if report.AddressesAI() {
		t.Error("a wildcard disallow was reported as an AI directive")
	}
	if len(report.AIAgentsBlocked) != 0 {
		t.Errorf("blocked = %v, want none", report.AIAgentsBlocked)
	}
	if !report.Groups[0].BlanketDisallow {
		t.Error("Disallow: / was not recognised as blanket")
	}
}

// "Disallow: / plus an Allow" is a site saying "only here", not "go away".
// Reading it as a blanket block would report a restriction the site did not make.
func TestParseRobotsDisallowWithAllowIsNotBlanket(t *testing.T) {
	report := ParseRobots("User-agent: GPTBot\nDisallow: /\nAllow: /blog/\n")

	if report.Groups[0].BlanketDisallow {
		t.Error("Disallow: / with an Allow was treated as a blanket block")
	}
	if len(report.AIAgentsBlocked) != 0 {
		t.Errorf("blocked = %v, want none — the agent may still fetch /blog/", report.AIAgentsBlocked)
	}
	if !report.AddressesAI() {
		t.Error("the file names GPTBot, so it addresses AI")
	}
}

// A rule following a rule starts a new group when a user-agent line intervenes.
// Merging a permissive default into a restrictive AI section — or the reverse —
// misreports the file in the most consequential way available.
func TestParseRobotsDoesNotMergeGroups(t *testing.T) {
	report := ParseRobots(`
User-agent: *
Disallow:

User-agent: GPTBot
Disallow: /
`)

	if len(report.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(report.Groups))
	}
	if report.Groups[0].BlanketDisallow {
		t.Error("the wildcard group inherited the AI group's blanket disallow")
	}
	if !report.Groups[1].BlanketDisallow {
		t.Error("the GPTBot group lost its blanket disallow")
	}
}

func TestParseRobotsEmptyIsStillAStatement(t *testing.T) {
	report := ParseRobots("")

	if !report.Present {
		t.Error("an empty robots.txt that was served is still present")
	}
	if report.AddressesAI() {
		t.Error("an empty file addresses nobody")
	}
}

func TestParseRobotsIgnoresComments(t *testing.T) {
	report := ParseRobots("# User-agent: GPTBot\n# Disallow: /\nUser-agent: *\nDisallow: /x\n")

	if report.AddressesAI() {
		t.Error("a commented-out directive was parsed as real")
	}
	if len(report.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(report.Groups))
	}
}

// A rule before any user-agent line governs nobody and must not be attached to
// whatever group happens to come next.
func TestParseRobotsIgnoresOrphanRules(t *testing.T) {
	report := ParseRobots("Disallow: /orphan\nUser-agent: GPTBot\nDisallow: /\n")

	if len(report.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(report.Groups))
	}
	if len(report.Groups[0].Disallow) != 1 || report.Groups[0].Disallow[0] != "/" {
		t.Errorf("orphan rule leaked into the group: %v", report.Groups[0].Disallow)
	}
}

func TestAIVendors(t *testing.T) {
	report := ParseRobots("User-agent: GPTBot\nUser-agent: chatgpt-user\nDisallow: /\n")

	vendors := report.AIVendors(report.AIAgentsBlocked)
	if len(vendors) != 1 || vendors[0] != "OpenAI" {
		t.Errorf("vendors = %v, want [OpenAI] — two OpenAI agents are one vendor", vendors)
	}
}

func TestSummariseDirectives(t *testing.T) {
	s := SummariseDirectives(ParseRobots("User-agent: CCBot\nDisallow: /\n"))

	if !s.RobotsPresent {
		t.Error("RobotsPresent is false")
	}
	if len(s.AgentsBlocked) != 1 || s.AgentsBlocked[0] != "ccbot" {
		t.Errorf("AgentsBlocked = %v", s.AgentsBlocked)
	}
	if len(s.VendorsBlocked) != 1 || s.VendorsBlocked[0] != "Common Crawl" {
		t.Errorf("VendorsBlocked = %v", s.VendorsBlocked)
	}
}

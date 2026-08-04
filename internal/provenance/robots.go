package provenance

import (
	"sort"
	"strings"
)

// aiAgents are the user-agent tokens that carry meaning about AI use specifically,
// as opposed to search indexing.
//
// A list like this dates, and that is survivable: an agent missing from it is
// reported under its own name in Groups anyway, so the log stays complete. What
// the list buys is the summary — being able to say "this site addressed AI
// crawlers" without the reader having to know which names count this year.
//
// being correct, not a string wanting a name; a constant would just be a second
// name for the same literal.
//
//nolint:goconst // a vendor appearing beside several of its agents is the table
var aiAgents = map[string]string{
	"gptbot":             "OpenAI",
	"chatgpt-user":       "OpenAI",
	"oai-searchbot":      "OpenAI",
	"ccbot":              "Common Crawl",
	"google-extended":    "Google",
	"anthropic-ai":       "Anthropic",
	"claudebot":          "Anthropic",
	"claude-web":         "Anthropic",
	"applebot-extended":  "Apple",
	"perplexitybot":      "Perplexity",
	"perplexity-user":    "Perplexity",
	"bytespider":         "ByteDance",
	"amazonbot":          "Amazon",
	"meta-externalagent": "Meta",
	"facebookbot":        "Meta",
	"cohere-ai":          "Cohere",
	"diffbot":            "Diffbot",
	"omgili":             "Webz.io",
	"timpibot":           "Timpi",
	"youbot":             "You.com",
}

func isAIAgentToken(name string) bool {
	_, ok := aiAgents[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// RobotsGroup is one user-agent section of a robots.txt.
type RobotsGroup struct {
	// Agents are the user-agent tokens this group applies to, lowercased. A group
	// can name several.
	Agents []string `json:"agents"`

	Allow    []string `json:"allow,omitempty"`
	Disallow []string `json:"disallow,omitempty"`

	// BlanketDisallow is `Disallow: /` with nothing allowed back — the shape of a
	// site turning an agent away entirely, and the one worth naming because it is
	// the common way to say no to AI crawlers.
	BlanketDisallow bool `json:"blanket_disallow,omitempty"`
}

// RobotsReport summarises what a robots.txt says, for the record rather than for
// enforcement.
//
// This parses robots.txt a second time, which wants justifying. The enforcement
// parser in internal/engine answers one question — may *this crawler* fetch *this
// URL* — and correctly discards everything else, including every group aimed at
// somebody else. Provenance needs exactly what enforcement throws away. Widening
// the enforcement parser to keep it would put a reporting concern inside the code
// path that decides whether a fetch is permitted, which is the last place to add
// incidental complexity. TestReportAgreesWithEnforcement keeps the two honest
// about the groups they both care about.
type RobotsReport struct {
	// Groups is every user-agent section found, in the order encountered.
	Groups []RobotsGroup `json:"groups,omitempty"`

	// AIAgentsAddressed lists the AI-related agents this file names, sorted.
	// Present at all means the site has expressed something about AI use.
	AIAgentsAddressed []string `json:"ai_agents_addressed,omitempty"`

	// AIAgentsBlocked lists those given a blanket disallow, sorted.
	AIAgentsBlocked []string `json:"ai_agents_blocked,omitempty"`

	// Sitemaps declared in the file.
	Sitemaps []string `json:"sitemaps,omitempty"`

	// Present distinguishes "fetched a robots.txt that said nothing" from "there
	// was no robots.txt". Both are permissive; only one is a statement.
	Present bool `json:"present"`
}

// AddressesAI reports whether the file names any AI-related crawler at all.
func (r RobotsReport) AddressesAI() bool { return len(r.AIAgentsAddressed) > 0 }

// ParseRobots builds a report from robots.txt content.
//
// An empty body still yields Present: a served-but-empty robots.txt is a site
// that answered the question with "no restrictions", which is different from a
// site that has no robots.txt at all — pass a report with Present false for that.
func ParseRobots(content string) RobotsReport {
	report := RobotsReport{Present: true}

	var (
		current    *RobotsGroup
		lastWasUA  bool
		addressed  = map[string]bool{}
		blockedSet = map[string]bool{}
	)

	for _, raw := range strings.Split(content, "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		field = strings.ToLower(strings.TrimSpace(field))
		value = strings.TrimSpace(value)

		switch field {
		case "user-agent":
			agent := strings.ToLower(value)

			// Consecutive User-agent lines share one group of rules; a User-agent
			// line after a rule starts a new group. Getting this wrong merges a
			// site's permissive default into a restrictive AI section, or the
			// reverse, and either way misreports what the site actually said.
			if current == nil || !lastWasUA {
				report.Groups = append(report.Groups, RobotsGroup{})
				current = &report.Groups[len(report.Groups)-1]
			}
			current.Agents = append(current.Agents, agent)
			if isAIAgentToken(agent) {
				addressed[agent] = true
			}
			lastWasUA = true
			continue

		case "sitemap":
			if value != "" {
				report.Sitemaps = append(report.Sitemaps, value)
			}
			continue
		}

		lastWasUA = false
		if current == nil {
			continue // a rule before any user-agent line applies to nobody
		}

		switch field {
		case "allow":
			if value != "" {
				current.Allow = append(current.Allow, value)
			}
		case "disallow":
			current.Disallow = append(current.Disallow, value)
		}
	}

	for i := range report.Groups {
		g := &report.Groups[i]
		g.BlanketDisallow = isBlanketDisallow(*g)
		if !g.BlanketDisallow {
			continue
		}
		for _, a := range g.Agents {
			if isAIAgentToken(a) {
				blockedSet[a] = true
			}
		}
	}

	report.AIAgentsAddressed = sortedKeys(addressed)
	report.AIAgentsBlocked = sortedKeys(blockedSet)
	return report
}

// isBlanketDisallow reports whether a group turns its agents away from everything.
//
// `Disallow: /` with any Allow line is not blanket — a site that blocks the root
// and re-permits a subtree has not said no, it has said "only here".
func isBlanketDisallow(g RobotsGroup) bool {
	if len(g.Allow) > 0 {
		return false
	}
	for _, d := range g.Disallow {
		if strings.TrimSpace(d) == "/" {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AIVendors maps the addressed agent tokens to the organisations behind them,
// deduplicated and sorted. Useful in a report, where "blocked OpenAI, Anthropic,
// and Common Crawl" reads better than a list of agent strings.
func (r RobotsReport) AIVendors(agents []string) []string {
	seen := map[string]bool{}
	for _, a := range agents {
		if v, ok := aiAgents[a]; ok {
			seen[v] = true
		}
	}
	return sortedKeys(seen)
}

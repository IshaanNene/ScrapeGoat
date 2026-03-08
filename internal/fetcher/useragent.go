package fetcher

import (
	"math/rand"
	"sync/atomic"
)

// UserAgentPool maintains a large pool of realistic user agent strings
// organized by browser family. Provides weighted random selection favoring
// common browsers to mimic real traffic patterns.
type UserAgentPool struct {
	agents []weightedUA
	total  int
	index  atomic.Int64
}

type weightedUA struct {
	ua     string
	weight int
}

// NewUserAgentPool creates a pool with 50+ real user agents.
func NewUserAgentPool() *UserAgentPool {
	pool := &UserAgentPool{}

	// Chrome (most popular — highest weight)
	chromeAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 11.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 11.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
	}
	for _, ua := range chromeAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 5})
		pool.total += 5
	}

	// Firefox
	firefoxAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:121.0) Gecko/20100101 Firefox/121.0",
		"Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0",
		"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:119.0) Gecko/20100101 Firefox/119.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:119.0) Gecko/20100101 Firefox/119.0",
		"Mozilla/5.0 (X11; Linux x86_64; rv:119.0) Gecko/20100101 Firefox/119.0",
	}
	for _, ua := range firefoxAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 3})
		pool.total += 3
	}

	// Safari
	safariAgents := []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Safari/605.1.15",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (iPad; CPU OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
	}
	for _, ua := range safariAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 2})
		pool.total += 2
	}

	// Edge
	edgeAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36 Edg/121.0.0.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36 Edg/119.0.0.0",
	}
	for _, ua := range edgeAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 2})
		pool.total += 2
	}

	// Opera
	operaAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 OPR/106.0.0.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 OPR/106.0.0.0",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 OPR/106.0.0.0",
	}
	for _, ua := range operaAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 1})
		pool.total += 1
	}

	// Vivaldi
	vivaldiAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Vivaldi/6.5",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Vivaldi/6.5",
	}
	for _, ua := range vivaldiAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 1})
		pool.total += 1
	}

	// Brave
	braveAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Brave/1.61",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Brave/1.61",
	}
	for _, ua := range braveAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 1})
		pool.total += 1
	}

	// Bot-like (low weight — for testing)
	botAgents := []string{
		"ScrapeGoat/1.0 (+https://github.com/IshaanNene/ScrapeGoat)",
		"Mozilla/5.0 (compatible; ScrapeGoat/1.0; +https://github.com/IshaanNene/ScrapeGoat)",
	}
	for _, ua := range botAgents {
		pool.agents = append(pool.agents, weightedUA{ua: ua, weight: 0})
	}

	return pool
}

// Random returns a weighted-random user agent string.
func (p *UserAgentPool) Random() string {
	if p.total == 0 {
		return p.agents[rand.Intn(len(p.agents))].ua
	}

	r := rand.Intn(p.total)
	cumulative := 0
	for _, wua := range p.agents {
		cumulative += wua.weight
		if r < cumulative {
			return wua.ua
		}
	}
	return p.agents[0].ua
}

// RoundRobin returns user agents in round-robin order.
func (p *UserAgentPool) RoundRobin() string {
	idx := p.index.Add(1)
	return p.agents[idx%int64(len(p.agents))].ua
}

// Count returns the total number of user agents in the pool.
func (p *UserAgentPool) Count() int {
	return len(p.agents)
}

// All returns all user agent strings.
func (p *UserAgentPool) All() []string {
	agents := make([]string, len(p.agents))
	for i, wua := range p.agents {
		agents[i] = wua.ua
	}
	return agents
}

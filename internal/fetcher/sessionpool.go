package fetcher

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http/cookiejar"
	"sync"
	"sync/atomic"
	"time"
)

// SessionPool maintains a pool of browser-like sessions, each with its own
// cookies, user agent, and optional proxy. Sessions are automatically rotated
// to distribute requests across multiple identities.
type SessionPool struct {
	sessions    []*PooledSession
	maxSessions int
	maxAge      time.Duration
	mu          sync.RWMutex
	logger      *slog.Logger
	index       atomic.Int64
	uaPool      *UserAgentPool
}

// PooledSession represents a single browser-like identity.
type PooledSession struct {
	ID        string
	UserAgent string
	CookieJar *cookiejar.Jar
	Proxy     string
	CreatedAt time.Time
	LastUsed  time.Time
	Requests  int64
	mu        sync.Mutex
}

// SessionPoolConfig configures the session pool.
type SessionPoolConfig struct {
	MaxSessions    int
	MaxSessionAge  time.Duration
	MaxRequestsPer int
	Proxies        []string
}

// DefaultSessionPoolConfig returns sensible defaults.
func DefaultSessionPoolConfig() *SessionPoolConfig {
	return &SessionPoolConfig{
		MaxSessions:    10,
		MaxSessionAge:  30 * time.Minute,
		MaxRequestsPer: 100,
	}
}

// NewSessionPool creates a new session pool.
func NewSessionPool(cfg *SessionPoolConfig, logger *slog.Logger) *SessionPool {
	if cfg == nil {
		cfg = DefaultSessionPoolConfig()
	}

	pool := &SessionPool{
		maxSessions: cfg.MaxSessions,
		maxAge:      cfg.MaxSessionAge,
		logger:      logger.With("component", "session_pool"),
		uaPool:      NewUserAgentPool(),
	}

	// Pre-create sessions
	for i := 0; i < cfg.MaxSessions; i++ {
		proxy := ""
		if len(cfg.Proxies) > 0 {
			proxy = cfg.Proxies[i%len(cfg.Proxies)]
		}
		pool.sessions = append(pool.sessions, pool.createSession(proxy))
	}

	logger.Info("session pool initialized", "sessions", len(pool.sessions))
	return pool
}

// Get returns a session using round-robin selection.
func (sp *SessionPool) Get() *PooledSession {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	if len(sp.sessions) == 0 {
		return sp.createSession("")
	}

	idx := sp.index.Add(1) % int64(len(sp.sessions))
	session := sp.sessions[idx]

	// Check if session needs rotation
	session.mu.Lock()
	if time.Since(session.CreatedAt) > sp.maxAge {
		session.mu.Unlock()
		return sp.rotateSession(int(idx))
	}
	session.LastUsed = time.Now()
	session.Requests++
	session.mu.Unlock()

	return session
}

// GetRandom returns a random session.
func (sp *SessionPool) GetRandom() *PooledSession {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	if len(sp.sessions) == 0 {
		return sp.createSession("")
	}

	session := sp.sessions[rand.Intn(len(sp.sessions))]
	session.mu.Lock()
	session.LastUsed = time.Now()
	session.Requests++
	session.mu.Unlock()

	return session
}

// rotateSession replaces an expired session with a fresh one.
func (sp *SessionPool) rotateSession(idx int) *PooledSession {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	old := sp.sessions[idx]
	proxy := old.Proxy

	newSession := sp.createSession(proxy)
	sp.sessions[idx] = newSession

	sp.logger.Debug("session rotated",
		"old_id", old.ID,
		"new_id", newSession.ID,
		"old_requests", old.Requests,
	)

	return newSession
}

// createSession creates a new session with a random identity.
func (sp *SessionPool) createSession(proxy string) *PooledSession {
	jar, _ := cookiejar.New(nil)
	return &PooledSession{
		ID:        fmt.Sprintf("session-%d", time.Now().UnixNano()),
		UserAgent: sp.uaPool.Random(),
		CookieJar: jar,
		Proxy:     proxy,
		CreatedAt: time.Now(),
		LastUsed:  time.Now(),
	}
}

// Stats returns session pool statistics.
func (sp *SessionPool) Stats() map[string]any {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	totalRequests := int64(0)
	oldestAge := time.Duration(0)

	for _, s := range sp.sessions {
		s.mu.Lock()
		totalRequests += s.Requests
		age := time.Since(s.CreatedAt)
		if age > oldestAge {
			oldestAge = age
		}
		s.mu.Unlock()
	}

	return map[string]any{
		"active_sessions": len(sp.sessions),
		"total_requests":  totalRequests,
		"oldest_session":  oldestAge.String(),
		"max_sessions":    sp.maxSessions,
	}
}

// Count returns the number of active sessions.
func (sp *SessionPool) Count() int {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return len(sp.sessions)
}

// Close cleans up all sessions.
func (sp *SessionPool) Close() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.sessions = nil
	sp.logger.Info("session pool closed")
}

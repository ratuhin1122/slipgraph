package httpserver

import (
	"net"
	"sync"
	"time"
)

type ipBudget struct {
	tokens  float64
	updated time.Time
}
type ipLimiter struct {
	mu          sync.Mutex
	entries     map[string]ipBudget
	now         func() time.Time
	rate, burst float64
	max         int
}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{entries: map[string]ipBudget{}, now: time.Now, rate: 100, burst: 1200, max: 4096}
}
func (l *ipLimiter) allow(remote string) bool {
	host, _, e := net.SplitHostPort(remote)
	if e != nil {
		host = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, exists := l.entries[host]
	if !exists {
		if len(l.entries) >= l.max {
			for key, v := range l.entries {
				if now.Sub(v.updated) > time.Minute {
					delete(l.entries, key)
				}
			}
		}
		if len(l.entries) >= l.max {
			return false
		}
		b = ipBudget{tokens: l.burst, updated: now}
	}
	b.tokens = min(l.burst, b.tokens+max(0, now.Sub(b.updated).Seconds())*l.rate)
	b.updated = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.entries[host] = b
	return allowed
}

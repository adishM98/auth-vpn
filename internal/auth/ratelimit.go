package auth

import (
	"sync"
	"time"
)

const (
	// maxTrackedIPs caps the number of IPs tracked in the attempts map.
	// If the cap is reached, the oldest entry is evicted to prevent memory exhaustion
	// from an attacker spoofing many source IPs.
	maxTrackedIPs = 10_000
)

// RateLimiter blocks IPs that fail authentication too many times.
type RateLimiter struct {
	mu          sync.Mutex
	attempts    map[string][]time.Time
	bans        map[string]time.Time
	order       []string // insertion order for LRU eviction under the cap
	maxAttempts int
	window      time.Duration
	banDuration time.Duration
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		attempts:    make(map[string][]time.Time),
		bans:        make(map[string]time.Time),
		maxAttempts: 5,
		window:      time.Minute,
		banDuration: 5 * time.Minute,
	}
	go rl.purgeLoop()
	return rl
}

// purgeLoop periodically removes expired bans and stale attempt records so the
// maps don't grow indefinitely even when the IP cap is not reached.
func (r *RateLimiter) purgeLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		r.purge()
	}
}

func (r *RateLimiter) purge() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	// Remove expired bans.
	for ip, until := range r.bans {
		if now.After(until) {
			delete(r.bans, ip)
		}
	}

	// Trim stale attempt windows.
	for ip, times := range r.attempts {
		var fresh []time.Time
		for _, t := range times {
			if now.Sub(t) < r.window {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			delete(r.attempts, ip)
		} else {
			r.attempts[ip] = fresh
		}
	}

	// Rebuild order to match current keys.
	newOrder := r.order[:0]
	for _, ip := range r.order {
		if _, ok := r.attempts[ip]; ok {
			newOrder = append(newOrder, ip)
		}
	}
	r.order = newOrder
}

// IsBanned returns true if ip is currently banned.
func (r *RateLimiter) IsBanned(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if until, ok := r.bans[ip]; ok {
		if time.Now().Before(until) {
			return true
		}
		delete(r.bans, ip)
	}
	return false
}

// RecordFailure records a failed auth attempt and bans if threshold exceeded.
func (r *RateLimiter) RecordFailure(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	// Enforce cap: evict the oldest tracked IP before adding a new one.
	if _, exists := r.attempts[ip]; !exists {
		for len(r.attempts) >= maxTrackedIPs && len(r.order) > 0 {
			oldest := r.order[0]
			r.order = r.order[1:]
			delete(r.attempts, oldest)
		}
		r.order = append(r.order, ip)
	}

	var recent []time.Time
	for _, t := range r.attempts[ip] {
		if now.Sub(t) < r.window {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	r.attempts[ip] = recent

	if len(recent) >= r.maxAttempts {
		r.bans[ip] = now.Add(r.banDuration)
		delete(r.attempts, ip)
	}
}

// Reset clears the failure count for ip (on successful auth).
func (r *RateLimiter) Reset(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.attempts, ip)
	delete(r.bans, ip)
}

package auth

import (
	"sync"
	"time"
)

// RateLimiter blocks IPs that fail authentication too many times.
type RateLimiter struct {
	mu          sync.Mutex
	attempts    map[string][]time.Time
	bans        map[string]time.Time
	maxAttempts int
	window      time.Duration
	banDuration time.Duration
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		attempts:    make(map[string][]time.Time),
		bans:        make(map[string]time.Time),
		maxAttempts: 5,
		window:      time.Minute,
		banDuration: time.Minute,
	}
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

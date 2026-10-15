package main

import (
	"math"
	"sync"
	"time"
)

type rateBucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

type IPRateLimiter struct {
	mu           sync.Mutex
	clients      map[string]*rateBucket
	tokensPerSec float64
	burst        float64
	lastCleanup  time.Time
	cleanupAfter time.Duration
}

func NewIPRateLimiter(requests int, window time.Duration, burst int) *IPRateLimiter {
	return &IPRateLimiter{
		clients:      make(map[string]*rateBucket),
		tokensPerSec: float64(requests) / window.Seconds(),
		burst:        float64(burst),
		cleanupAfter: 10 * time.Minute,
	}
}

func (l *IPRateLimiter) Allow(ip string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
		for key, bucket := range l.clients {
			if now.Sub(bucket.lastSeen) > l.cleanupAfter {
				delete(l.clients, key)
			}
		}
		l.lastCleanup = now
	}

	bucket, ok := l.clients[ip]
	if !ok {
		bucket = &rateBucket{tokens: l.burst, last: now, lastSeen: now}
		l.clients[ip] = bucket
	}

	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 {
		bucket.tokens = math.Min(l.burst, bucket.tokens+elapsed*l.tokensPerSec)
		bucket.last = now
	}
	bucket.lastSeen = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}
	retry := time.Duration(math.Ceil((1-bucket.tokens)/l.tokensPerSec*1000)) * time.Millisecond
	return false, retry
}

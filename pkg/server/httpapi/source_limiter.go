package httpapi

import (
	"sync"
	"time"
)

const maxSourceLimiterEntries = 10_000

// Fallbacks for a limiter built without a configured budget. Server
// configuration validation rejects a non-positive limit, so these only apply
// when a handler is constructed directly. They exist because neither
// degenerate reading of "no budget configured" is acceptable: treating it as
// zero attempts silently disables the endpoint, and treating it as unlimited
// silently removes the protection.
const (
	defaultSourceLimitWindow      = time.Minute
	defaultSourceLimitMaxAttempts = 30
)

type sourceLimiter struct {
	mutex       sync.Mutex
	window      time.Duration
	maxAttempts int
	entries     map[string]sourceLimitEntry
}

type sourceLimitEntry struct {
	windowStarted time.Time
	count         int
}

func newSourceLimiter(window time.Duration, maxAttempts int) *sourceLimiter {
	if window <= 0 {
		window = defaultSourceLimitWindow
	}
	if maxAttempts <= 0 {
		maxAttempts = defaultSourceLimitMaxAttempts
	}
	return &sourceLimiter{
		window:      window,
		maxAttempts: maxAttempts,
		entries:     make(map[string]sourceLimitEntry),
	}
}

func (limiter *sourceLimiter) allow(
	source string,
	now time.Time,
) (bool, time.Duration) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	entry, exists := limiter.entries[source]
	if !exists || !now.Before(entry.windowStarted.Add(limiter.window)) {
		limiter.ensureCapacity(now)
		entry = sourceLimitEntry{windowStarted: now}
	}
	if entry.count >= limiter.maxAttempts {
		return false, sourceRetryAfter(entry, limiter.window, now)
	}
	entry.count++
	limiter.entries[source] = entry
	return true, 0
}

func (limiter *sourceLimiter) ensureCapacity(now time.Time) {
	if len(limiter.entries) < maxSourceLimiterEntries {
		return
	}
	var oldestSource string
	var oldestTime time.Time
	for source, entry := range limiter.entries {
		if !now.Before(entry.windowStarted.Add(limiter.window)) {
			delete(limiter.entries, source)
			continue
		}
		if oldestSource == "" || entry.windowStarted.Before(oldestTime) {
			oldestSource = source
			oldestTime = entry.windowStarted
		}
	}
	if len(limiter.entries) >= maxSourceLimiterEntries && oldestSource != "" {
		delete(limiter.entries, oldestSource)
	}
}

func sourceRetryAfter(
	entry sourceLimitEntry,
	window time.Duration,
	now time.Time,
) time.Duration {
	remaining := entry.windowStarted.Add(window).Sub(now)
	if remaining <= 0 {
		return time.Second
	}
	return remaining
}

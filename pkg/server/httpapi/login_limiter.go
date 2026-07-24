package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const maxLoginLimiterEntries = 10_000

type loginLimiterConfig struct {
	window                time.Duration
	maxAttemptsPerAccount int
	maxAttemptsPerSource  int
}

type loginLimiter struct {
	mutex   sync.Mutex
	config  loginLimiterConfig
	entries map[string]loginLimitEntry
}

type loginLimitEntry struct {
	windowStarted time.Time
	count         int
	denialAudited bool
}

func newLoginLimiter(config loginLimiterConfig) *loginLimiter {
	return &loginLimiter{
		config:  config,
		entries: make(map[string]loginLimitEntry),
	}
}

func (limiter *loginLimiter) allow(
	source string,
	account string,
	now time.Time,
) (bool, time.Duration, bool) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	sourceKey := loginLimitKey("source", source)
	sourceEntry := limiter.currentEntry(sourceKey, now)
	if sourceEntry.count >= limiter.config.maxAttemptsPerSource {
		shouldAudit := !sourceEntry.denialAudited
		sourceEntry.denialAudited = true
		limiter.entries[sourceKey] = sourceEntry
		return false, retryAfter(sourceEntry, limiter.config.window, now), shouldAudit
	}

	accountKey := loginLimitKey("account", account)
	accountEntry := limiter.currentEntry(accountKey, now)
	if accountEntry.count >= limiter.config.maxAttemptsPerAccount {
		shouldAudit := !accountEntry.denialAudited
		accountEntry.denialAudited = true
		limiter.entries[accountKey] = accountEntry
		return false, retryAfter(accountEntry, limiter.config.window, now), shouldAudit
	}

	sourceEntry.count++
	accountEntry.count++
	limiter.entries[sourceKey] = sourceEntry
	limiter.entries[accountKey] = accountEntry
	return true, 0, false
}

func (limiter *loginLimiter) currentEntry(key string, now time.Time) loginLimitEntry {
	entry, exists := limiter.entries[key]
	if !exists || !now.Before(entry.windowStarted.Add(limiter.config.window)) {
		limiter.ensureCapacity(now)
		return loginLimitEntry{windowStarted: now}
	}
	return entry
}

func (limiter *loginLimiter) ensureCapacity(now time.Time) {
	if len(limiter.entries) < maxLoginLimiterEntries {
		return
	}

	var oldestKey string
	var oldestTime time.Time
	for key, entry := range limiter.entries {
		if !now.Before(entry.windowStarted.Add(limiter.config.window)) {
			delete(limiter.entries, key)
			continue
		}
		if oldestKey == "" || entry.windowStarted.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.windowStarted
		}
	}
	if len(limiter.entries) >= maxLoginLimiterEntries && oldestKey != "" {
		delete(limiter.entries, oldestKey)
	}
}

func retryAfter(entry loginLimitEntry, window time.Duration, now time.Time) time.Duration {
	remaining := entry.windowStarted.Add(window).Sub(now)
	if remaining <= 0 {
		return time.Second
	}
	return remaining
}

func loginLimitKey(kind string, value string) string {
	digest := sha256.Sum256([]byte(value))
	return kind + ":" + hex.EncodeToString(digest[:])
}

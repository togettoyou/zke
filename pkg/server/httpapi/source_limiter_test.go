package httpapi

import (
	"testing"
	"time"
)

func TestSourceLimiterResetsAfterWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	limiter := newSourceLimiter(time.Minute, 2)
	for attempt := 0; attempt < 2; attempt++ {
		if allowed, _ := limiter.allow("127.0.0.1", now); !allowed {
			t.Fatalf("attempt %d was unexpectedly rejected", attempt+1)
		}
	}
	if allowed, retry := limiter.allow("127.0.0.1", now); allowed || retry <= 0 {
		t.Fatalf("limited attempt = %t/%s, want false/positive retry", allowed, retry)
	}
	if allowed, _ := limiter.allow("127.0.0.1", now.Add(time.Minute)); !allowed {
		t.Fatal("attempt was not allowed after the rate limit window")
	}
}

// An unconfigured budget must not read as "zero attempts allowed", which would
// disable the endpoint it guards instead of protecting it.
func TestSourceLimiterFallsBackToADefaultBudget(t *testing.T) {
	t.Parallel()

	now := time.Now()
	limiter := newSourceLimiter(0, 0)
	if allowed, _ := limiter.allow("127.0.0.1", now); !allowed {
		t.Fatal("an unconfigured limiter rejected the first attempt")
	}
	for range defaultSourceLimitMaxAttempts {
		limiter.allow("127.0.0.1", now)
	}
	if allowed, _ := limiter.allow("127.0.0.1", now); allowed {
		t.Fatal("an unconfigured limiter never applied a budget")
	}
}

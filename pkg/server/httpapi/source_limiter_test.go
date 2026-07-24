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

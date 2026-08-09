package httpapi

import (
	"testing"
	"time"
)

func TestPodAccessSessionTTL(t *testing.T) {
	t.Parallel()
	if got := podAccessSessionTTL(nil); got != 15*time.Minute {
		t.Fatalf("omitted session TTL = %s, want 15m", got)
	}
	for _, seconds := range []int64{900, 1800, 3600} {
		if got := podAccessSessionTTL(&seconds); got != time.Duration(seconds)*time.Second {
			t.Fatalf("session TTL for %d seconds = %s", seconds, got)
		}
	}
	for _, seconds := range []int64{-1, 0, 1, 2700, 3601, 1 << 62} {
		if got := podAccessSessionTTL(&seconds); got != 0 {
			t.Fatalf("unsupported session TTL for %d seconds = %s, want zero", seconds, got)
		}
	}
}

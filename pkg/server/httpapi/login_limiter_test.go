package httpapi

import (
	"testing"
	"time"
)

func TestLoginLimiterAppliesAccountAndSourceLimits(t *testing.T) {
	t.Parallel()

	limiter := newLoginLimiter(loginLimiterConfig{
		window:                time.Minute,
		maxAttemptsPerAccount: 2,
		maxAttemptsPerSource:  3,
	})
	now := time.Now()

	if allowed, _, _ := limiter.allow("source-1", "account-1", now); !allowed {
		t.Fatal("first login attempt was denied")
	}
	if allowed, _, _ := limiter.allow("source-2", "account-1", now); !allowed {
		t.Fatal("second login attempt was denied")
	}
	if allowed, retry, audit := limiter.allow(
		"source-3",
		"account-1",
		now,
	); allowed || retry <= 0 || !audit {
		t.Fatal("account login limit was not enforced")
	}
	if _, _, audit := limiter.allow("source-3", "account-1", now); audit {
		t.Fatal("account denial was audited more than once in the same window")
	}

	if allowed, _, _ := limiter.allow("source-4", "account-2", now); !allowed {
		t.Fatal("independent account and source were denied")
	}
	if allowed, _, _ := limiter.allow("source-4", "account-3", now); !allowed {
		t.Fatal("second source attempt was denied")
	}
	if allowed, _, _ := limiter.allow("source-4", "account-4", now); !allowed {
		t.Fatal("third source attempt was denied")
	}
	if allowed, retry, audit := limiter.allow(
		"source-4",
		"account-5",
		now,
	); allowed || retry <= 0 || !audit {
		t.Fatal("source login limit was not enforced")
	}
}

func TestLoginLimiterResetsAfterWindow(t *testing.T) {
	t.Parallel()

	limiter := newLoginLimiter(loginLimiterConfig{
		window:                time.Minute,
		maxAttemptsPerAccount: 1,
		maxAttemptsPerSource:  1,
	})
	now := time.Now()

	if allowed, _, _ := limiter.allow("source", "account", now); !allowed {
		t.Fatal("first login attempt was denied")
	}
	if allowed, _, _ := limiter.allow("source", "account", now.Add(30*time.Second)); allowed {
		t.Fatal("login attempt inside the limit window was allowed")
	}
	if allowed, _, _ := limiter.allow("source", "account", now.Add(time.Minute)); !allowed {
		t.Fatal("login attempt after the limit window was denied")
	}
}

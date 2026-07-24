package agentstatus

import (
	"testing"
	"time"
)

func TestCertificateStatusPriority(t *testing.T) {
	t.Parallel()

	revokedAt := time.Now()
	for _, test := range []struct {
		name      string
		lifecycle string
		revokedAt *time.Time
		remaining time.Duration
		want      string
	}{
		{"valid", "active", nil, 31 * 24 * time.Hour, "valid"},
		{"expiring", "active", nil, 30 * 24 * time.Hour, "expiring"},
		{"expired", "active", nil, -time.Second, "expired"},
		{"credential revoked", "active", &revokedAt, -time.Second, "revoked"},
		{"agent revoked", "revoked", nil, 31 * 24 * time.Hour, "revoked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := certificateStatus(
				test.lifecycle,
				test.revokedAt,
				test.remaining,
				30*24*time.Hour,
			); got != test.want {
				t.Fatalf("certificateStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

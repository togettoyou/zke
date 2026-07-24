package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestServiceUpgradesOutdatedPasswordHash(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	password := []byte("a sufficiently long admin passphrase")
	outdatedParams := auth.DefaultPasswordParams()
	outdatedParams.Iterations--
	outdatedHash, err := auth.HashPassword(password, outdatedParams)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
)
VALUES (gen_random_uuid(), 'rehash-user', 'Rehash User', $1, 'active', now())
RETURNING id::text
`, outdatedHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	service := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	if _, err := service.Login(ctx, auth.LoginInput{
		Username:  "rehash-user",
		Password:  password,
		RequestID: "request-rehash-login",
		Now:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var upgradedHash string
	if err := pool.QueryRow(
		ctx,
		"SELECT password_hash FROM users WHERE id = $1",
		userID,
	).Scan(&upgradedHash); err != nil {
		t.Fatal(err)
	}
	if upgradedHash == outdatedHash {
		t.Fatal("login did not upgrade outdated Argon2 parameters")
	}
	matches, needsRehash, err := auth.VerifyPassword(password, upgradedHash)
	if err != nil {
		t.Fatal(err)
	}
	if !matches || needsRehash {
		t.Fatalf("upgraded password verification = %v/%v, want true/false", matches, needsRehash)
	}
}

func TestServiceRejectsConcurrentCredentialChange(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	password := []byte("a sufficiently long admin passphrase")
	passwordHash, err := auth.HashPassword(password, auth.DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	changedPasswordHash, err := auth.HashPassword(
		[]byte("a different sufficiently long passphrase"),
		auth.DefaultPasswordParams(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
)
VALUES (gen_random_uuid(), 'concurrent-user', 'Concurrent User', $1, 'active', now())
RETURNING id::text
`, passwordHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	lockTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = lockTransaction.Rollback(context.Background())
	}()
	if _, err := lockTransaction.Exec(
		ctx,
		"SELECT id FROM users WHERE id = $1 FOR UPDATE",
		userID,
	); err != nil {
		t.Fatal(err)
	}

	service := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	loginResult := make(chan error, 1)
	go func() {
		_, err := service.Login(ctx, auth.LoginInput{
			Username:  "concurrent-user",
			Password:  password,
			RequestID: "request-concurrent-login",
			Now:       time.Now().UTC(),
		})
		loginResult <- err
	}()

	var applicationName string
	if err := pool.QueryRow(ctx, "SHOW application_name").Scan(&applicationName); err != nil {
		t.Fatal(err)
	}
	waitForBlockedCredentialCheck(t, ctx, pool, applicationName)

	if _, err := lockTransaction.Exec(ctx, `
UPDATE users
SET
    password_hash = $2,
    password_changed_at = clock_timestamp()
WHERE id = $1
`, userID, changedPasswordHash); err != nil {
		t.Fatal(err)
	}
	if err := lockTransaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-loginResult; !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("concurrent credential change error = %v, want ErrInvalidCredentials", err)
	}
	var sessionCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM user_sessions WHERE user_id = $1",
		userID,
	).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 {
		t.Fatalf("concurrent credential change created %d sessions, want 0", sessionCount)
	}
	var failedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE target_id = $1
  AND action = 'auth.login'
  AND result = 'failed'
`, userID).Scan(&failedAuditCount); err != nil {
		t.Fatal(err)
	}
	if failedAuditCount != 1 {
		t.Fatalf("failed login audit count = %d, want 1", failedAuditCount)
	}
}

func waitForBlockedCredentialCheck(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	applicationName string,
) {
	t.Helper()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_stat_activity
    WHERE application_name = $1
      AND wait_event_type = 'Lock'
      AND query LIKE '%password_hash%'
      AND query LIKE '%FOR UPDATE%'
)
`, applicationName).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("login did not block on the credential row lock")
		case <-ticker.C:
		}
	}
}

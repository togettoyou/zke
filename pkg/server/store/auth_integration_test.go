package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestInitialAdminAndSessionStore(t *testing.T) {
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	authStore := store.NewAuthStore(pool)
	password := []byte("a sufficiently long admin passphrase")
	user, err := auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username:    "  Ａdmin  ",
		DisplayName: "ZKE Administrator",
		Password:    password,
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.UsernameNormalized != "admin" || user.Status != "active" {
		t.Fatalf("created user = %#v", user)
	}
	matches, needsRehash, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("stored administrator password hash did not verify")
	}
	if needsRehash {
		t.Fatal("new administrator password hash unexpectedly needs an upgrade")
	}

	_, err = auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username:    "second-admin",
		DisplayName: "Second Administrator",
		Password:    []byte("another sufficiently long passphrase"),
	})
	if !errors.Is(err, store.ErrInitialAdminExists) {
		t.Fatalf("second initial administrator error = %v, want ErrInitialAdminExists", err)
	}

	var role, scopeType, auditAction, auditActorID string
	if err := pool.QueryRow(ctx, `
SELECT role, scope_type
FROM role_bindings
WHERE subject_id = $1
`, user.ID).Scan(&role, &scopeType); err != nil {
		t.Fatal(err)
	}
	if role != "admin" || scopeType != "global" {
		t.Fatalf("initial role = %s/%s, want admin/global", role, scopeType)
	}
	if err := pool.QueryRow(ctx, `
SELECT action, actor_user_id::text
FROM audit_events
WHERE target_id = $1
`, user.ID).Scan(&auditAction, &auditActorID); err != nil {
		t.Fatal(err)
	}
	if auditAction != "auth.initial_admin.create" || auditActorID != user.ID {
		t.Fatalf("initial audit = %s/%s", auditAction, auditActorID)
	}

	foundUser, err := authStore.FindUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if foundUser.ID != user.ID {
		t.Fatalf("found user ID = %s, want %s", foundUser.ID, user.ID)
	}

	_, tokenDigest, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	idleTimeout := 30 * time.Minute
	session, err := authStore.CreateSession(ctx, store.CreateSessionParams{
		UserID:        user.ID,
		TokenDigest:   tokenDigest,
		IdleExpiresAt: now.Add(idleTimeout),
		ExpiresAt:     now.Add(35 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	activityTime := now.Add(10 * time.Minute)
	identity, err := authStore.FindActiveSession(ctx, tokenDigest, activityTime, idleTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Session.ID != session.ID || identity.User.ID != user.ID {
		t.Fatalf("session identity = %#v", identity)
	}
	expectedIdleExpiry := session.ExpiresAt
	if !identity.Session.LastSeenAt.Equal(activityTime) {
		t.Fatalf(
			"session last seen = %s, want %s",
			identity.Session.LastSeenAt,
			activityTime,
		)
	}
	if !identity.Session.IdleExpiresAt.Equal(expectedIdleExpiry) {
		t.Fatalf(
			"session idle expiry = %s, want %s",
			identity.Session.IdleExpiresAt,
			expectedIdleExpiry,
		)
	}

	if _, err := pool.Exec(ctx, "UPDATE users SET status = 'disabled' WHERE id = $1", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := authStore.FindActiveSession(
		ctx,
		tokenDigest,
		activityTime,
		idleTimeout,
	); !errors.Is(
		err,
		store.ErrSessionNotFound,
	) {
		t.Fatalf("disabled user session lookup error = %v, want ErrSessionNotFound", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET status = 'active' WHERE id = $1", user.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE users SET password_changed_at = clock_timestamp() WHERE id = $1",
		user.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := authStore.FindActiveSession(
		ctx,
		tokenDigest,
		activityTime,
		idleTimeout,
	); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("pre-password-change session lookup error = %v, want ErrSessionNotFound", err)
	}

	_, secondTokenDigest, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	secondSessionTime := time.Now().UTC().Truncate(time.Microsecond)
	secondSession, err := authStore.CreateSession(ctx, store.CreateSessionParams{
		UserID:        user.ID,
		TokenDigest:   secondTokenDigest,
		IdleExpiresAt: secondSessionTime.Add(idleTimeout),
		ExpiresAt:     secondSessionTime.Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authStore.FindActiveSession(
		ctx,
		secondTokenDigest,
		secondSessionTime,
		idleTimeout,
	); err != nil {
		t.Fatalf("post-password-change session lookup: %v", err)
	}

	revoked, err := authStore.RevokeSession(
		ctx,
		secondSession.ID,
		secondSessionTime.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("active session was not revoked")
	}
	if _, err := authStore.FindActiveSession(
		ctx,
		secondTokenDigest,
		secondSessionTime.Add(2*time.Minute),
		idleTimeout,
	); !errors.Is(
		err,
		store.ErrSessionNotFound,
	) {
		t.Fatalf("revoked session lookup error = %v, want ErrSessionNotFound", err)
	}
}

func openIsolatedDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	var randomValue [8]byte
	if _, err := rand.Read(randomValue[:]); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	schemaName := "zke_auth_test_" + hex.EncodeToString(randomValue[:])
	quotedSchemaName := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchemaName+" CASCADE")
		adminPool.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchemaName+" CASCADE")
		adminPool.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(
			cleanupContext,
			"DROP SCHEMA "+quotedSchemaName+" CASCADE",
		); err != nil {
			t.Errorf("drop integration test schema: %v", err)
		}
		adminPool.Close()
	})
	return pool
}

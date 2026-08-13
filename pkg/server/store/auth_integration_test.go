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
	applyMigrations(t, ctx, pool)

	authStore := store.NewAuthStore(pool)
	password := []byte("a sufficiently long admin passphrase")
	user, err := auth.CreateFirstGlobalAdministrator(ctx, authStore, auth.FirstGlobalAdministratorInput{
		Username:    "  Ａdmin  ",
		DisplayName: "ZKE Administrator",
		Password:    password,
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "admin" {
		t.Fatalf("created user = %#v", user)
	}
	var storedInitialPasswordHash, storedInitialStatus string
	if err := pool.QueryRow(
		ctx,
		"SELECT password_hash, status FROM users WHERE id = $1",
		user.ID,
	).Scan(&storedInitialPasswordHash, &storedInitialStatus); err != nil {
		t.Fatal(err)
	}
	if storedInitialStatus != "active" {
		t.Fatalf("created user status = %q, want active", storedInitialStatus)
	}
	matches, needsRehash, err := auth.VerifyPassword(password, storedInitialPasswordHash)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("stored administrator password hash did not verify")
	}
	if needsRehash {
		t.Fatal("new administrator password hash unexpectedly needs an upgrade")
	}

	_, err = auth.CreateFirstGlobalAdministrator(ctx, authStore, auth.FirstGlobalAdministratorInput{
		Username:    "second-admin",
		DisplayName: "Second Administrator",
		Password:    []byte("another sufficiently long passphrase"),
	})
	if !errors.Is(err, auth.ErrSetupAlreadyCompleted) {
		t.Fatalf("second global administrator setup error = %v, want ErrSetupAlreadyCompleted", err)
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
	if auditAction != "auth.administrator.setup" || auditActorID != user.ID {
		t.Fatalf("initial audit = %s/%s", auditAction, auditActorID)
	}

	foundUser, err := authStore.FindUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if foundUser.ID != user.ID {
		t.Fatalf("found user ID = %s, want %s", foundUser.ID, user.ID)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	idleTimeout := 30 * time.Minute
	authenticationService := auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout:          idleTimeout,
		SessionAbsoluteTimeout:      35 * time.Minute,
		MaxConcurrentPasswordChecks: 1,
	})
	loginResult, err := authenticationService.Login(ctx, auth.LoginInput{
		Username:  "admin",
		Password:  password,
		RequestID: "request-login-1",
		Now:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenDigest := auth.DigestSessionToken(loginResult.SessionToken)
	storedLoginIdentity, err := authStore.FindActiveSession(ctx, tokenDigest, now, idleTimeout)
	if err != nil {
		t.Fatal(err)
	}
	session := storedLoginIdentity.Session
	if !auth.CSRFTokenMatches(loginResult.CSRFToken, session.CSRFTokenDigest) {
		t.Fatal("login CSRF token does not match the stored digest")
	}
	var loginAuditAction, loginAuditResult string
	if err := pool.QueryRow(ctx, `
SELECT action, result
FROM audit_events
WHERE target_id = $1
`, session.ID).Scan(&loginAuditAction, &loginAuditResult); err != nil {
		t.Fatal(err)
	}
	if loginAuditAction != "auth.login" || loginAuditResult != "succeeded" {
		t.Fatalf("login audit = %s/%s", loginAuditAction, loginAuditResult)
	}
	serviceIdentity, err := authenticationService.Authenticate(
		ctx,
		loginResult.SessionToken,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if serviceIdentity.User.ID != user.ID ||
		serviceIdentity.User.Username != "admin" ||
		serviceIdentity.SessionID != session.ID {
		t.Fatalf("service identity = %#v", serviceIdentity)
	}
	if !authenticationService.CSRFTokenMatches(serviceIdentity, loginResult.CSRFToken) {
		t.Fatal("authentication service rejected the login CSRF token")
	}

	_, err = authenticationService.Login(ctx, auth.LoginInput{
		Username:  "missing-user",
		Password:  password,
		RequestID: "request-login-missing",
		Now:       now,
	})
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("missing-user login error = %v, want ErrInvalidCredentials", err)
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
	_, err = authenticationService.Login(ctx, auth.LoginInput{
		Username:  "admin",
		Password:  password,
		RequestID: "request-login-disabled",
		Now:       activityTime,
	})
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("disabled-user login error = %v, want ErrInvalidCredentials", err)
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

	changedPasswordHash, err := auth.HashPassword(password, auth.DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`
UPDATE users
SET
    password_hash = $2,
    password_changed_at = clock_timestamp()
WHERE id = $1
`,
		user.ID,
		changedPasswordHash,
	); err != nil {
		t.Fatal(err)
	}
	_, staleSessionTokenDigest, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	_, staleCSRFTokenDigest, err := auth.NewCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	_, err = authStore.CompleteLogin(ctx, store.CompleteLoginParams{
		UserID:                    foundUser.ID,
		ExpectedPasswordHash:      foundUser.PasswordHash,
		ExpectedPasswordChangedAt: foundUser.PasswordChangedAt,
		ReplacementPasswordHash:   foundUser.PasswordHash,
		Session: store.CreateSessionParams{
			UserID:          foundUser.ID,
			TokenDigest:     staleSessionTokenDigest,
			CSRFTokenDigest: staleCSRFTokenDigest,
			IdleExpiresAt:   activityTime.Add(idleTimeout),
			ExpiresAt:       activityTime.Add(8 * time.Hour),
		},
		RequestID: "request-stale-login",
	})
	if !errors.Is(err, store.ErrCredentialsChanged) {
		t.Fatalf("stale credential login error = %v, want ErrCredentialsChanged", err)
	}
	var storedPasswordHash string
	if err := pool.QueryRow(
		ctx,
		"SELECT password_hash FROM users WHERE id = $1",
		user.ID,
	).Scan(&storedPasswordHash); err != nil {
		t.Fatal(err)
	}
	if storedPasswordHash != changedPasswordHash {
		t.Fatal("stale login overwrote the concurrently changed password hash")
	}
	if _, err := authStore.FindActiveSession(
		ctx,
		tokenDigest,
		activityTime,
		idleTimeout,
	); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("pre-password-change session lookup error = %v, want ErrSessionNotFound", err)
	}

	secondSessionTime := time.Now().UTC().Truncate(time.Microsecond)
	authenticationService = auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout:          idleTimeout,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	secondLoginResult, err := authenticationService.Login(ctx, auth.LoginInput{
		Username:  "admin",
		Password:  password,
		RequestID: "request-login-2",
		Now:       secondSessionTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondTokenDigest := auth.DigestSessionToken(secondLoginResult.SessionToken)
	secondIdentity, err := authStore.FindActiveSession(
		ctx,
		secondTokenDigest,
		secondSessionTime,
		idleTimeout,
	)
	if err != nil {
		t.Fatalf("post-password-change session lookup: %v", err)
	}
	secondSession := secondIdentity.Session
	secondServiceIdentity, err := authenticationService.Authenticate(
		ctx,
		secondLoginResult.SessionToken,
		secondSessionTime,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = authenticationService.Logout(
		ctx,
		secondServiceIdentity,
		"request-logout-1",
		secondSessionTime.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
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
	var logoutAuditAction, logoutAuditResult string
	if err := pool.QueryRow(ctx, `
SELECT action, result
FROM audit_events
WHERE target_id = $1
  AND action = 'auth.logout'
`, secondSession.ID).Scan(&logoutAuditAction, &logoutAuditResult); err != nil {
		t.Fatal(err)
	}
	if logoutAuditAction != "auth.logout" || logoutAuditResult != "succeeded" {
		t.Fatalf("logout audit = %s/%s", logoutAuditAction, logoutAuditResult)
	}
}

func requireAuthTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}
	return databaseURL
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
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schemaName
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

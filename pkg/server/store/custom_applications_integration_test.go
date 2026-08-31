package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	customApplicationTenant   = "00000000-0000-0000-0000-0000000002a1"
	customApplicationProject  = "00000000-0000-0000-0000-0000000002a2"
	customApplicationProject2 = "00000000-0000-0000-0000-0000000002a3"
	customApplicationAuthor   = "00000000-0000-0000-0000-0000000002a4"
)

func seedCustomApplicationFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Now().UTC()
	for _, statement := range []struct {
		sql       string
		arguments []any
	}{
		{`INSERT INTO tenants (id, name, status) VALUES ($1, 'applications', 'active')`, []any{customApplicationTenant}},
		{`INSERT INTO projects (id, tenant_id, name, status) VALUES ($1, $2, 'applications', 'active')`, []any{customApplicationProject, customApplicationTenant}},
		{`INSERT INTO projects (id, tenant_id, name, status) VALUES ($1, $2, 'applications-two', 'active')`, []any{customApplicationProject2, customApplicationTenant}},
		{`INSERT INTO users (id, username_normalized, display_name, password_hash, status, password_changed_at)
          VALUES ($1, 'application-author', 'Application Author', 'not-used', 'active', $2)`, []any{customApplicationAuthor, now}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.arguments...); err != nil {
			t.Fatalf("seed custom application fixture: %v", err)
		}
	}
}

func createCustomApplication(
	t *testing.T,
	ctx context.Context,
	applications *store.CustomApplicationStore,
	id string,
	projectID string,
	name string,
	key string,
) (store.CustomApplication, error) {
	t.Helper()
	return applications.CreateCustomApplication(ctx, store.CreateCustomApplicationParams{
		ID: id, ProjectID: projectID, CreatedByUserID: customApplicationAuthor,
		Name: name, URL: "https://apps.example.test/entry",
		IdempotencyKey: key, Now: time.Now().UTC().Truncate(time.Microsecond),
	})
}

func TestCustomApplicationsAreProjectScopedAndIdempotent(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	seedCustomApplicationFixture(t, ctx, pool)
	applications := store.NewCustomApplicationStore(pool)

	id := "00000000-0000-0000-0000-0000000002b1"
	created, err := createCustomApplication(
		t, ctx, applications, id, customApplicationProject, "Harbor", "custom-application-key-0001",
	)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := createCustomApplication(
		t, ctx, applications, "00000000-0000-0000-0000-0000000002b2",
		customApplicationProject, "Harbor", "custom-application-key-0001",
	)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("idempotent replay = (%s, %v), want original %s", replayed.ID, err, created.ID)
	}
	if _, err := applications.GetCustomApplication(ctx, customApplicationProject2, id); !errors.Is(err, store.ErrCustomApplicationNotFound) {
		t.Fatalf("cross-Project read error = %v, want not found", err)
	}
	if _, err := createCustomApplication(
		t, ctx, applications, "00000000-0000-0000-0000-0000000002b3",
		customApplicationProject, "Grafana", "custom-application-key-0001",
	); !errors.Is(err, store.ErrCustomApplicationIdempotencyConflict) {
		t.Fatalf("reused idempotency key error = %v, want conflict", err)
	}
	if _, err := createCustomApplication(
		t, ctx, applications, "00000000-0000-0000-0000-0000000002b4",
		customApplicationProject, "harbor", "custom-application-key-0002",
	); !errors.Is(err, store.ErrCustomApplicationConflict) {
		t.Fatalf("case-insensitive name collision error = %v, want conflict", err)
	}
}

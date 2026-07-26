package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestResourceCreationIsAtomicAndIdempotent(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actorID := insertRBACUser(t, ctx, pool, "resource-creator")
	resourceStore := store.NewResourceManagementStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	tenantResults := make(chan store.CreateTenantResult, 2)
	tenantErrors := make(chan error, 2)
	for _, id := range []string{
		"10000000-0000-4000-8000-000000000001",
		"10000000-0000-4000-8000-000000000002",
	} {
		go func() {
			result, err := resourceStore.CreateTenant(ctx, store.CreateTenantParams{
				ID:             id,
				Name:           "Concurrent Tenant",
				ActorUserID:    actorID,
				RequestID:      "request-" + id,
				IdempotencyKey: "concurrent-tenant-key",
				Now:            now,
			})
			tenantResults <- result
			tenantErrors <- err
		}()
	}
	var tenantID string
	replayCount := 0
	for range 2 {
		if err := <-tenantErrors; err != nil {
			t.Fatal(err)
		}
		result := <-tenantResults
		if result.Replayed {
			replayCount++
		}
		if tenantID == "" {
			tenantID = result.Tenant.ID
		} else if result.Tenant.ID != tenantID {
			t.Fatalf("concurrent tenant IDs differ: %s and %s", tenantID, result.Tenant.ID)
		}
	}
	if replayCount != 1 {
		t.Fatalf("tenant replay count = %d, want 1", replayCount)
	}

	projectResults := make(chan store.CreateProjectResult, 2)
	projectErrors := make(chan error, 2)
	for _, id := range []string{
		"20000000-0000-4000-8000-000000000001",
		"20000000-0000-4000-8000-000000000002",
	} {
		go func() {
			result, err := resourceStore.CreateProject(ctx, store.CreateProjectParams{
				ID:             id,
				TenantID:       tenantID,
				Name:           "Concurrent Project",
				ActorUserID:    actorID,
				RequestID:      "request-" + id,
				IdempotencyKey: "concurrent-project-key",
				Now:            now,
			})
			projectResults <- result
			projectErrors <- err
		}()
	}
	var projectID string
	replayCount = 0
	for range 2 {
		if err := <-projectErrors; err != nil {
			t.Fatal(err)
		}
		result := <-projectResults
		if result.Replayed {
			replayCount++
		}
		if projectID == "" {
			projectID = result.Project.ID
		} else if result.Project.ID != projectID {
			t.Fatalf("concurrent project IDs differ: %s and %s", projectID, result.Project.ID)
		}
	}
	if replayCount != 1 {
		t.Fatalf("project replay count = %d, want 1", replayCount)
	}

	var tenantCount, projectCount, requestCount, auditCount int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM tenants WHERE name = 'Concurrent Tenant'),
    (SELECT count(*) FROM projects WHERE name = 'Concurrent Project'),
    (
        SELECT count(*) FROM tenant_creation_requests
        WHERE actor_user_id = $1
    ) + (
        SELECT count(*) FROM project_creation_requests
        WHERE actor_user_id = $1
    ),
    (
        SELECT count(*) FROM audit_events
        WHERE actor_user_id = $1
          AND action IN ('tenant.create', 'project.create')
          AND result = 'succeeded'
    )
`, actorID).Scan(
		&tenantCount,
		&projectCount,
		&requestCount,
		&auditCount,
	); err != nil {
		t.Fatal(err)
	}
	if tenantCount != 1 || projectCount != 1 ||
		requestCount != 2 || auditCount != 2 {
		t.Fatalf(
			"tenant/project/requests/audits = %d/%d/%d/%d, want 1/1/2/2",
			tenantCount,
			projectCount,
			requestCount,
			auditCount,
		)
	}
}

package store_test

import (
	"context"
	"errors"
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

// Tenant names are unique without regard to case, across every status, and the
// rule must not be reachable by way of a rename either.
//
// The idempotent-replay case is the one worth being careful about: a retry
// submits the same name a second time, so it arrives at the unique index
// looking exactly like a name that is already taken. It has to keep answering
// with the original Tenant instead.
func TestTenantNamesAreUniqueWithoutCase(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actorID := insertRBACUser(t, ctx, pool, "tenant-namer")
	resourceStore := store.NewResourceManagementStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	create := func(id string, name string, key string) (store.CreateTenantResult, error) {
		return resourceStore.CreateTenant(ctx, store.CreateTenantParams{
			ID:             id,
			Name:           name,
			ActorUserID:    actorID,
			RequestID:      "request-" + key,
			IdempotencyKey: key,
			Now:            now,
		})
	}

	first, err := create("30000000-0000-4000-8000-000000000001", "Acme", "tenant-name-key-0001")
	if err != nil {
		t.Fatal(err)
	}

	// Same name, and the same name in another case, both refused.
	for _, name := range []string{"Acme", "acme", "ACME"} {
		if _, err := create(
			"30000000-0000-4000-8000-000000000002",
			name,
			"tenant-name-key-"+name,
		); !errors.Is(err, store.ErrTenantNameConflict) {
			t.Fatalf("CreateTenant(%q) error = %v, want ErrTenantNameConflict", name, err)
		}
	}

	// Suspension holds the name. It destroys nothing and can be undone, so
	// releasing the name would let someone else take it before the original
	// Tenant is resumed.
	if _, err := resourceStore.UpdateTenant(ctx, store.UpdateTenantParams{
		TenantID:    first.Tenant.ID,
		Name:        first.Tenant.Name,
		Status:      "suspended",
		ActorUserID: actorID,
		RequestID:   "request-suspend-acme",
		Now:         now,
	}); err != nil {
		t.Fatal(err)
	}
	var suspensionAction string
	if err := pool.QueryRow(ctx, `
SELECT action
FROM audit_events
WHERE request_id = 'request-suspend-acme'
`).Scan(&suspensionAction); err != nil {
		t.Fatal(err)
	}
	if suspensionAction != "tenant.suspend" {
		t.Fatalf("Tenant suspension action = %q, want tenant.suspend", suspensionAction)
	}
	if _, err := create(
		"30000000-0000-4000-8000-000000000003",
		"acme",
		"tenant-name-key-while-suspended",
	); !errors.Is(err, store.ErrTenantNameConflict) {
		t.Fatalf("CreateTenant() while suspended error = %v, want ErrTenantNameConflict", err)
	}

	// A retry of the original submission still answers with the original
	// Tenant rather than reporting the name it created as taken.
	replay, err := create("30000000-0000-4000-8000-000000000004", "Acme", "tenant-name-key-0001")
	if err != nil {
		t.Fatalf("replayed CreateTenant() error = %v, want the original tenant", err)
	}
	if !replay.Replayed || replay.Tenant.ID != first.Tenant.ID {
		t.Fatalf(
			"replayed CreateTenant() = %+v, want the original %s",
			replay,
			first.Tenant.ID,
		)
	}

	// Renaming onto a taken name is refused on the same terms.
	other, err := create("30000000-0000-4000-8000-000000000005", "Globex", "tenant-name-key-0002")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resourceStore.UpdateTenant(ctx, store.UpdateTenantParams{
		TenantID:    other.Tenant.ID,
		Name:        "ACME",
		Status:      "active",
		ActorUserID: actorID,
		RequestID:   "request-rename-collision",
		Now:         now,
	}); !errors.Is(err, store.ErrTenantNameConflict) {
		t.Fatalf("UpdateTenant() onto a taken name error = %v, want ErrTenantNameConflict", err)
	}

	// Renaming to its own name in a different case is not a collision with
	// anyone else, so it must still be allowed.
	if _, err := resourceStore.UpdateTenant(ctx, store.UpdateTenantParams{
		TenantID:    other.Tenant.ID,
		Name:        "GLOBEX",
		Status:      "active",
		ActorUserID: actorID,
		RequestID:   "request-rename-own-case",
		Now:         now,
	}); err != nil {
		t.Fatalf("UpdateTenant() to its own name in another case: %v", err)
	}

	// Deletion frees the name, because it removes the row rather than marking
	// it. This is the whole difference between suspending and deleting.
	if _, err := resourceStore.DeleteTenant(ctx, store.DeleteTenantParams{
		TenantID:    first.Tenant.ID,
		ActorUserID: actorID,
		RequestID:   "request-delete-acme",
		Now:         now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := create(
		"30000000-0000-4000-8000-000000000006",
		"acme",
		"tenant-name-key-after-delete",
	); err != nil {
		t.Fatalf("CreateTenant() after the holder was deleted: %v", err)
	}
}

// Project names are unique inside their Tenant — and only inside it, so the
// same name in a second Tenant has to stay allowed.
func TestProjectNamesAreUniqueWithinTenant(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actorID := insertRBACUser(t, ctx, pool, "project-namer")
	firstTenant := insertRBACTenant(t, ctx, pool, "First Tenant")
	secondTenant := insertRBACTenant(t, ctx, pool, "Second Tenant")
	resourceStore := store.NewResourceManagementStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	create := func(id string, tenantID string, name string, key string) (store.CreateProjectResult, error) {
		return resourceStore.CreateProject(ctx, store.CreateProjectParams{
			ID:             id,
			TenantID:       tenantID,
			Name:           name,
			ActorUserID:    actorID,
			RequestID:      "request-" + key,
			IdempotencyKey: key,
			Now:            now,
		})
	}

	first, err := create("40000000-0000-4000-8000-000000000001", firstTenant, "Platform", "project-key-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create(
		"40000000-0000-4000-8000-000000000002",
		firstTenant,
		"PLATFORM",
		"project-key-0002",
	); !errors.Is(err, store.ErrProjectNameConflict) {
		t.Fatalf("CreateProject() error = %v, want ErrProjectNameConflict", err)
	}

	// Another Tenant is a different namespace; two organizations both having a
	// "platform" is ordinary.
	if _, err := create(
		"40000000-0000-4000-8000-000000000003",
		secondTenant,
		"platform",
		"project-key-0003",
	); err != nil {
		t.Fatalf("CreateProject() in another tenant: %v", err)
	}

	// A retry still answers with the original Project.
	replay, err := create("40000000-0000-4000-8000-000000000004", firstTenant, "Platform", "project-key-0001")
	if err != nil {
		t.Fatalf("replayed CreateProject() error = %v", err)
	}
	if !replay.Replayed || replay.Project.ID != first.Project.ID {
		t.Fatalf("replayed CreateProject() = %+v, want the original %s", replay, first.Project.ID)
	}

	other, err := create("40000000-0000-4000-8000-000000000005", firstTenant, "Edge", "project-key-0004")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resourceStore.UpdateProject(ctx, store.UpdateProjectParams{
		ProjectID:   other.Project.ID,
		Name:        "platform",
		Status:      "active",
		ActorUserID: actorID,
		RequestID:   "request-project-rename-collision",
		Now:         now,
	}); !errors.Is(err, store.ErrProjectNameConflict) {
		t.Fatalf("UpdateProject() onto a taken name error = %v, want ErrProjectNameConflict", err)
	}
}

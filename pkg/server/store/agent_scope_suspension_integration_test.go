package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

func TestAgentConnectionStoreWatchesParentScopeSuspensions(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	const (
		tenantID  = "71000000-0000-4000-8000-000000000001"
		projectID = "71000000-0000-4000-8000-000000000002"
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO tenants (id, name, status)
VALUES ($1, 'scope suspension tenant', 'active')
`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES ($2, $1, 'scope suspension project', 'active')
`, tenantID, projectID); err != nil {
		t.Fatal(err)
	}

	watchContext, cancelWatch := context.WithCancel(ctx)
	ready := make(chan struct{})
	events := make(chan store.AgentConnectionRevocation, 4)
	watchErrors := make(chan error, 1)
	go func() {
		watchErrors <- store.NewAgentConnectionStore(pool).WatchRevocations(
			watchContext,
			func() { close(ready) },
			func(event store.AgentConnectionRevocation) {
				events <- event
			},
		)
	}()
	select {
	case <-ready:
	case err := <-watchErrors:
		t.Fatalf("start revocation watcher: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE projects SET status = 'suspended' WHERE id = $1",
		projectID,
	); err != nil {
		t.Fatal(err)
	}
	waitForScopeSuspension(t, ctx, events, watchErrors, "", projectID)

	if _, err := pool.Exec(
		ctx,
		"UPDATE tenants SET status = 'suspended' WHERE id = $1",
		tenantID,
	); err != nil {
		t.Fatal(err)
	}
	waitForScopeSuspension(t, ctx, events, watchErrors, tenantID, "")

	cancelWatch()
	select {
	case err := <-watchErrors:
		if err != nil {
			t.Fatalf("stop revocation watcher: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func waitForScopeSuspension(
	t *testing.T,
	ctx context.Context,
	events <-chan store.AgentConnectionRevocation,
	watchErrors <-chan error,
	tenantID string,
	projectID string,
) {
	t.Helper()

	for {
		select {
		case event := <-events:
			if event.TenantID == tenantID &&
				event.ProjectID == projectID &&
				event.Reason == "scope_suspended" {
				return
			}
		case err := <-watchErrors:
			t.Fatalf("revocation watcher stopped before scope event: %v", err)
		case <-ctx.Done():
			t.Fatalf(
				"no suspension notification for Tenant %q Project %q: %v",
				tenantID,
				projectID,
				ctx.Err(),
			)
		}
	}
}

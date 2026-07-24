package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestRecordProjectAuditEventPreservesScope(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	tenantID := insertRBACTenant(t, ctx, pool, "Audit Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Audit Project")
	userID := insertRBACUser(t, ctx, pool, "audit-user")
	auditStore := store.NewAuditStore(pool)
	if err := auditStore.RecordProjectEvent(ctx, store.ProjectAuditEvent{
		ActorUserID: userID,
		ProjectID:   projectID,
		Action:      "agent.enrollment.create",
		Result:      "denied",
		RequestID:   "request-project-audit",
	}); err != nil {
		t.Fatal(err)
	}

	var scopeType, storedTenantID, storedProjectID string
	if err := pool.QueryRow(ctx, `
SELECT scope_type, tenant_id::text, project_id::text
FROM audit_events
WHERE request_id = 'request-project-audit'
`).Scan(&scopeType, &storedTenantID, &storedProjectID); err != nil {
		t.Fatal(err)
	}
	if scopeType != "project" ||
		storedTenantID != tenantID ||
		storedProjectID != projectID {
		t.Fatalf(
			"audit scope = %s/%s/%s, want project/%s/%s",
			scopeType,
			storedTenantID,
			storedProjectID,
			tenantID,
			projectID,
		)
	}
}

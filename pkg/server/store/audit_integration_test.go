package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/auditctx"
)

func TestRecordProjectAuditEventPreservesScope(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	tenantID := insertRBACTenant(t, ctx, pool, "Audit Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Audit Project")
	userID := insertRBACUser(t, ctx, pool, "audit-user")
	auditStore := store.NewAuditStore(pool)
	if err := auditStore.RecordProjectEvent(ctx, store.ProjectAuditEvent{
		ActorUserID: userID,
		ProjectID:   projectID,
		ProjectName: "Audit Project",
		Action:      "cluster.enrollment.create",
		TargetType:  "enrollment",
		TargetName:  "requested-cluster",
		Result:      "denied",
		RequestID:   "request-project-audit",
	}); err != nil {
		t.Fatal(err)
	}

	var scopeType, storedTenantID, storedProjectID, projectName, targetName string
	if err := pool.QueryRow(ctx, `
SELECT scope_type, tenant_id::text, project_id::text, project_name, target_name
FROM audit_events
WHERE request_id = 'request-project-audit'
`).Scan(
		&scopeType,
		&storedTenantID,
		&storedProjectID,
		&projectName,
		&targetName,
	); err != nil {
		t.Fatal(err)
	}
	if scopeType != "project" ||
		storedTenantID != tenantID ||
		storedProjectID != projectID ||
		projectName != "Audit Project" ||
		targetName != "requested-cluster" {
		t.Fatalf(
			"audit scope = %s/%s/%s (%q -> %q), want project/%s/%s (Audit Project -> requested-cluster)",
			scopeType,
			storedTenantID,
			storedProjectID,
			projectName,
			targetName,
			tenantID,
			projectID,
		)
	}
}

func TestRecordSuccessfulClusterAuditEvent(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	tenantID := insertRBACTenant(t, ctx, pool, "Kubernetes Audit Tenant")
	projectID := insertRBACProject(
		t,
		ctx,
		pool,
		tenantID,
		"Kubernetes Audit Project",
	)
	clusterID := insertRBACCluster(
		t,
		ctx,
		pool,
		tenantID,
		projectID,
		"Kubernetes Audit Cluster",
	)
	userID := insertRBACUser(t, ctx, pool, "kubernetes-audit-user")
	auditStore := store.NewAuditStore(pool)
	if err := auditStore.RecordClusterEvent(ctx, store.ClusterAuditEvent{
		ActorUserID: userID,
		ClusterID:   clusterID,
		Action:      "kubernetes_resource.create",
		TargetType:  "kubernetes_resource",
		TargetName:  "core/v1/namespaces/model-serving",
		Result:      "succeeded",
		RequestID:   "request-kubernetes-audit",
	}); err != nil {
		t.Fatal(err)
	}

	var scopeType, storedClusterID, action, targetName, result string
	if err := pool.QueryRow(ctx, `
SELECT scope_type, cluster_id::text, action, target_name, result
FROM audit_events
WHERE request_id = 'request-kubernetes-audit'
`).Scan(
		&scopeType,
		&storedClusterID,
		&action,
		&targetName,
		&result,
	); err != nil {
		t.Fatal(err)
	}
	if scopeType != "cluster" ||
		storedClusterID != clusterID ||
		action != "kubernetes_resource.create" ||
		targetName != "core/v1/namespaces/model-serving" ||
		result != "succeeded" {
		t.Fatalf(
			"successful Cluster audit = %s/%s %s %s %s",
			scopeType,
			storedClusterID,
			action,
			targetName,
			result,
		)
	}
}

// A denial is recorded after the decision has already been made, so the record
// must not depend on the caller still being connected. This exercises the
// composition the authorization middleware uses: a request context that is
// already cancelled, and the detached context derived from it.
func TestRecordAuditEventSurvivesRequestCancellation(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	tenantID := insertRBACTenant(t, ctx, pool, "Disconnect Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Disconnect Project")
	userID := insertRBACUser(t, ctx, pool, "disconnect-user")
	auditStore := store.NewAuditStore(pool)

	requestContext, cancelRequest := context.WithCancel(ctx)
	cancelRequest()

	// Same write on the raw request context first: it must fail, otherwise the
	// test would pass even if Detach did nothing.
	if err := auditStore.RecordProjectEvent(requestContext, store.ProjectAuditEvent{
		ActorUserID: userID,
		ProjectID:   projectID,
		Action:      "cluster.read",
		TargetType:  "cluster",
		Result:      "denied",
		RequestID:   "request-cancelled-directly",
	}); err == nil {
		t.Fatal("audit write on a cancelled request context unexpectedly succeeded")
	}

	auditContext, cancelAudit := auditctx.Detach(requestContext, 10*time.Second)
	defer cancelAudit()
	if err := auditStore.RecordProjectEvent(auditContext, store.ProjectAuditEvent{
		ActorUserID: userID,
		ProjectID:   projectID,
		Action:      "cluster.read",
		TargetType:  "cluster",
		Result:      "denied",
		RequestID:   "request-cancelled-detached",
	}); err != nil {
		t.Fatalf("audit write on a detached context failed: %v", err)
	}

	var recorded int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE request_id = 'request-cancelled-detached'
  AND action = 'cluster.read'
  AND result = 'denied'
`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("denial audit rows = %d, want 1", recorded)
	}
}

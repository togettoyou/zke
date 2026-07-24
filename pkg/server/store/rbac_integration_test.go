package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestRBACServiceScopesAndRoles(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	tenantA := insertRBACTenant(t, ctx, pool, "Tenant A")
	tenantB := insertRBACTenant(t, ctx, pool, "Tenant B")
	projectA1 := insertRBACProject(t, ctx, pool, tenantA, "Project A1")
	projectA2 := insertRBACProject(t, ctx, pool, tenantA, "Project A2")
	projectB1 := insertRBACProject(t, ctx, pool, tenantB, "Project B1")

	globalAdmin := insertRBACUser(t, ctx, pool, "global-admin")
	globalViewer := insertRBACUser(t, ctx, pool, "global-viewer")
	tenantAdmin := insertRBACUser(t, ctx, pool, "tenant-admin")
	projectViewer := insertRBACUser(t, ctx, pool, "project-viewer")
	unboundUser := insertRBACUser(t, ctx, pool, "unbound-user")

	insertRoleBinding(t, ctx, pool, globalAdmin, "admin", "global", "", "")
	insertRoleBinding(t, ctx, pool, globalViewer, "viewer", "global", "", "")
	insertRoleBinding(t, ctx, pool, tenantAdmin, "admin", "tenant", tenantA, "")
	insertRoleBinding(t, ctx, pool, projectViewer, "viewer", "project", tenantA, projectA1)

	rbacStore := store.NewRBACStore(pool)
	service := rbac.NewService(rbacStore)
	tests := []struct {
		name       string
		userID     string
		permission rbac.Permission
		scopeType  string
		tenantID   string
		projectID  string
		allowed    bool
	}{
		{
			name:       "global admin creates enrollment in any project",
			userID:     globalAdmin,
			permission: rbac.PermissionAgentEnrollmentCreate,
			scopeType:  "project",
			projectID:  projectB1,
			allowed:    true,
		},
		{
			name:       "global viewer reads cluster",
			userID:     globalViewer,
			permission: rbac.PermissionClusterRead,
			scopeType:  "project",
			projectID:  projectB1,
			allowed:    true,
		},
		{
			name:       "global viewer reads global cluster view",
			userID:     globalViewer,
			permission: rbac.PermissionClusterRead,
			scopeType:  "global",
			allowed:    true,
		},
		{
			name:       "global viewer cannot revoke agent",
			userID:     globalViewer,
			permission: rbac.PermissionAgentRevoke,
			scopeType:  "project",
			projectID:  projectB1,
		},
		{
			name:       "tenant admin manages its project",
			userID:     tenantAdmin,
			permission: rbac.PermissionAgentEnrollmentCreate,
			scopeType:  "project",
			projectID:  projectA2,
			allowed:    true,
		},
		{
			name:       "tenant admin reads its tenant",
			userID:     tenantAdmin,
			permission: rbac.PermissionAgentRead,
			scopeType:  "tenant",
			tenantID:   tenantA,
			allowed:    true,
		},
		{
			name:       "tenant admin cannot use global scope",
			userID:     tenantAdmin,
			permission: rbac.PermissionAgentRead,
			scopeType:  "global",
		},
		{
			name:       "tenant admin cannot cross tenant",
			userID:     tenantAdmin,
			permission: rbac.PermissionAgentEnrollmentCreate,
			scopeType:  "project",
			projectID:  projectB1,
		},
		{
			name:       "project viewer reads its project",
			userID:     projectViewer,
			permission: rbac.PermissionAgentRead,
			scopeType:  "project",
			projectID:  projectA1,
			allowed:    true,
		},
		{
			name:       "project viewer cannot read sibling project",
			userID:     projectViewer,
			permission: rbac.PermissionClusterRead,
			scopeType:  "project",
			projectID:  projectA2,
		},
		{
			name:       "project viewer cannot use tenant scope",
			userID:     projectViewer,
			permission: rbac.PermissionClusterRead,
			scopeType:  "tenant",
			tenantID:   tenantA,
		},
		{
			name:       "project viewer cannot create enrollment",
			userID:     projectViewer,
			permission: rbac.PermissionAgentEnrollmentCreate,
			scopeType:  "project",
			projectID:  projectA1,
		},
		{
			name:       "unbound user is denied",
			userID:     unboundUser,
			permission: rbac.PermissionClusterRead,
			scopeType:  "project",
			projectID:  projectA1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			switch test.scopeType {
			case "global":
				err = service.AuthorizeGlobal(ctx, test.userID, test.permission)
			case "tenant":
				err = service.AuthorizeTenant(
					ctx, test.userID, test.permission, test.tenantID,
				)
			case "project":
				err = service.AuthorizeProject(
					ctx, test.userID, test.permission, test.projectID,
				)
			default:
				t.Fatalf("unknown test scope type %q", test.scopeType)
			}
			if test.allowed && err != nil {
				t.Fatalf("authorization error = %v, want allowed", err)
			}
			if !test.allowed && !errors.Is(err, rbac.ErrDenied) {
				t.Fatalf("authorization error = %v, want ErrDenied", err)
			}
		})
	}

	if err := service.AuthorizeProject(
		ctx,
		projectViewer,
		rbac.PermissionClusterRead,
		projectA1,
	); err != nil {
		t.Fatalf("AuthorizeProject() error = %v, want allowed", err)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE users SET status = 'disabled' WHERE id = $1",
		projectViewer,
	); err != nil {
		t.Fatal(err)
	}
	if err := service.AuthorizeProject(
		ctx,
		projectViewer,
		rbac.PermissionClusterRead,
		projectA1,
	); !errors.Is(err, rbac.ErrDenied) {
		t.Fatalf("disabled user authorization error = %v, want ErrDenied", err)
	}
	if err := service.AuthorizeProject(
		ctx,
		globalAdmin,
		rbac.PermissionClusterRead,
		"00000000-0000-0000-0000-000000000001",
	); !errors.Is(err, rbac.ErrDenied) {
		t.Fatalf("missing project error = %v, want ErrDenied", err)
	}

	canceledContext, cancelQuery := context.WithCancel(ctx)
	cancelQuery()
	if _, err := rbacStore.ListRoleBindings(
		canceledContext,
		globalAdmin,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled role binding query error = %v, want context.Canceled", err)
	}
	if _, err := rbacStore.FindProjectTenant(
		canceledContext,
		projectA1,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled project query error = %v, want context.Canceled", err)
	}
}

func insertRBACUser(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	username string,
) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
)
VALUES (gen_random_uuid(), $1, $1, 'not-used', 'active', now())
RETURNING id::text
`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRBACTenant(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	name string,
) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), $1, 'active')
RETURNING id::text
`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRBACProject(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID string,
	name string,
) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, $2, 'active')
RETURNING id::text
`, tenantID, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRoleBinding(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	userID string,
	role string,
	scopeType string,
	tenantID string,
	projectID string,
) {
	t.Helper()
	var nullableTenant any
	if tenantID != "" {
		nullableTenant = tenantID
	}
	var nullableProject any
	if projectID != "" {
		nullableProject = projectID
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (
    id,
    subject_id,
    role,
    scope_type,
    tenant_id,
    project_id
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
`, userID, role, scopeType, nullableTenant, nullableProject); err != nil {
		t.Fatal(err)
	}
}

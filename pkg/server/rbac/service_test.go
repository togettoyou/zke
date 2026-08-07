package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	testUserID    = "00000000-0000-0000-0000-000000000001"
	testTenantID  = "00000000-0000-0000-0000-000000000002"
	testProjectID = "00000000-0000-0000-0000-000000000003"
	testClusterID = "00000000-0000-0000-0000-000000000007"
)

func TestServiceRejectsUnknownPermissionBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil)
	err := service.AuthorizeGlobal(
		context.Background(),
		testUserID,
		Permission("unknown"),
	)
	if !errors.Is(err, ErrUnknownPermission) {
		t.Fatalf("AuthorizeGlobal() error = %v, want ErrUnknownPermission", err)
	}

	_, err = service.AuthorizeProject(
		context.Background(),
		testUserID,
		Permission("unknown"),
		testProjectID,
	)
	if !errors.Is(err, ErrUnknownPermission) {
		t.Fatalf("AuthorizeProject() error = %v, want ErrUnknownPermission", err)
	}
}

func TestServiceRejectsInvalidIdentifiersBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil)
	tests := []struct {
		name      string
		authorize func() error
		want      error
	}{
		{
			name: "invalid subject",
			authorize: func() error {
				return service.AuthorizeGlobal(
					context.Background(),
					"not-a-uuid",
					PermissionClusterRead,
				)
			},
			want: ErrDenied,
		},
		{
			name: "invalid tenant",
			authorize: func() error {
				return service.AuthorizeTenant(
					context.Background(),
					testUserID,
					PermissionClusterRead,
					"not-a-uuid",
				)
			},
			want: ErrInvalidScope,
		},
		{
			name: "invalid project",
			authorize: func() error {
				_, err := service.AuthorizeProject(
					context.Background(),
					testUserID,
					PermissionClusterRead,
					"not-a-uuid",
				)
				return err
			},
			want: ErrInvalidScope,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.authorize(); !errors.Is(err, test.want) {
				t.Fatalf("authorization error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBuiltinRoleAndScopeRules(t *testing.T) {
	t.Parallel()

	targetScope := projectScope(testTenantID, testProjectID)
	if !builtinRoleGrants("admin", PermissionClusterConnectionRevoke) {
		t.Fatal("admin role did not grant cluster.connection.revoke")
	}
	if !builtinRoleGrants("viewer", PermissionClusterRead) {
		t.Fatal("viewer role did not grant cluster.read")
	}
	if builtinRoleGrants("viewer", PermissionClusterEnrollmentCreate) {
		t.Fatal("viewer role granted cluster.enrollment.create")
	}
	if !builtinRoleGrants("admin", PermissionClusterPodLogsRead) {
		t.Fatal("admin role did not grant cluster.pod.logs.read")
	}
	if builtinRoleGrants("viewer", PermissionClusterPodLogsRead) {
		t.Fatal("viewer role granted cluster.pod.logs.read")
	}
	if !builtinRoleGrants("admin", PermissionClusterEventRead) ||
		builtinRoleGrants("viewer", PermissionClusterEventRead) {
		t.Fatal("cluster.event.read must be restricted to admin")
	}
	if !builtinRoleGrants("admin", PermissionTenantCreate) ||
		!builtinRoleGrants("admin", PermissionProjectCreate) {
		t.Fatal("admin role did not grant resource creation permissions")
	}
	if builtinRoleGrants("viewer", PermissionTenantCreate) ||
		builtinRoleGrants("viewer", PermissionProjectCreate) {
		t.Fatal("viewer role granted resource creation permissions")
	}
	for _, permission := range []Permission{
		PermissionUserRead,
		PermissionUserManage,
		PermissionRBACRead,
		PermissionRBACManage,
		PermissionAuditRead,
		PermissionClusterRBACRead,
		PermissionClusterRBACManage,
		PermissionClusterSecretRead,
		PermissionClusterSecretManage,
	} {
		if !builtinRoleGrants("admin", permission) {
			t.Errorf("admin role did not grant %s", permission)
		}
		if builtinRoleGrants("viewer", permission) {
			t.Errorf("viewer role unexpectedly granted %s", permission)
		}
	}
	if !bindingApplies(store.RoleBinding{
		ScopeType: "tenant",
		TenantID:  testTenantID,
	}, targetScope) {
		t.Fatal("tenant binding did not apply to its project")
	}
	if bindingApplies(store.RoleBinding{
		ScopeType: "project",
		TenantID:  testTenantID,
		ProjectID: "00000000-0000-0000-0000-000000000004",
	}, targetScope) {
		t.Fatal("different project binding applied")
	}
}

// A scoped binding must not report permissions no scoped route ever checks.
//
// `me` is what a client builds its interface from, so a capability it lists is
// a claim that the operation is available. Listing `user.manage` on a Tenant
// binding claimed an operation that every route refuses, and the operator who
// bound the builtin `admin` role to a Tenant was shown an account holding
// everything.
func TestScopedCapabilitiesOmitGlobalOnlyPermissions(t *testing.T) {
	t.Parallel()

	service := NewService(bindingStub{bindings: []store.RoleBinding{
		{
			Role:        "admin",
			ScopeType:   "tenant",
			TenantID:    testTenantID,
			Permissions: permissionNames(Permissions()),
		},
		{
			Role:        "admin",
			ScopeType:   "global",
			Permissions: permissionNames(Permissions()),
		},
	}})

	capabilities, err := service.ListCapabilities(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("ListCapabilities() error = %v", err)
	}
	if len(capabilities) != 2 {
		t.Fatalf("capabilities = %d, want 2", len(capabilities))
	}

	scoped, global := capabilities[0], capabilities[1]
	if scoped.ScopeType != "tenant" || global.ScopeType != "global" {
		scoped, global = global, scoped
	}
	for _, permission := range scoped.Permissions {
		if GlobalOnly(permission) {
			t.Errorf("tenant capability reported global-only %s", permission)
		}
	}
	// The scoped binding keeps everything else, and the global one keeps all of
	// it: the filter is about reach, not about narrowing the role.
	if len(scoped.Permissions)+len(globalOnlyPermissions) != len(Permissions()) {
		t.Errorf(
			"tenant capability has %d permissions, want %d",
			len(scoped.Permissions),
			len(Permissions())-len(globalOnlyPermissions),
		)
	}
	if len(global.Permissions) != len(Permissions()) {
		t.Errorf(
			"global capability has %d permissions, want %d",
			len(global.Permissions),
			len(Permissions()),
		)
	}
}

type bindingStub struct {
	bindings []store.RoleBinding
}

func (stub bindingStub) ListRoleBindings(
	_ context.Context,
	_ string,
) ([]store.RoleBinding, error) {
	return stub.bindings, nil
}

func (stub bindingStub) FindProjectTenant(
	_ context.Context,
	_ string,
) (string, error) {
	return testTenantID, nil
}

func (stub bindingStub) FindClusterAuthorizationScope(
	_ context.Context,
	_ string,
) (store.ClusterAuthorizationScope, error) {
	return store.ClusterAuthorizationScope{
		TenantID:  testTenantID,
		ProjectID: testProjectID,
	}, nil
}

func permissionNames(permissions []Permission) []string {
	names := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		names = append(names, string(permission))
	}
	return names
}

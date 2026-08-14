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
	if !builtinRoleGrants("admin", PermissionClusterNodeDrain) ||
		builtinRoleGrants("viewer", PermissionClusterNodeDrain) {
		t.Fatal("cluster.node.drain must be restricted to admin")
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

// A binding must not report permissions its own scope cannot exercise.
//
// `me` is what a client builds its interface from, so a capability it lists is
// a claim that the operation is available. Listing `user.manage` on a Tenant
// binding claimed an operation that every route refuses, and the operator who
// bound the builtin `admin` role to a Tenant was shown an account holding
// everything. A Project binding has a second floor above it — `project.create`
// is checked with `RequireTenant` — so the same claim was being made one level
// down, where a boolean "global only" could not see it.
func TestCapabilitiesOmitPermissionsBelowTheBindingScope(t *testing.T) {
	t.Parallel()

	service := NewService(bindingStub{bindings: []store.RoleBinding{
		{
			Role:        "admin",
			ScopeType:   "project",
			TenantID:    testTenantID,
			ProjectID:   testProjectID,
			Permissions: permissionNames(Permissions()),
		},
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
	if len(capabilities) != 3 {
		t.Fatalf("capabilities = %d, want 3", len(capabilities))
	}

	byScope := make(map[string]Capability, len(capabilities))
	for _, capability := range capabilities {
		byScope[capability.ScopeType] = capability
	}

	for scopeName, capability := range byScope {
		for _, permission := range capability.Permissions {
			if InertAt(permission, scopeName) {
				t.Errorf(
					"%s capability reported %s, which that scope cannot exercise",
					scopeName,
					permission,
				)
			}
		}
	}

	// The counts come from the floors rather than from a literal, so adding a
	// permission does not silently make this assertion weaker. The filter is
	// about reach: nothing above the floor is dropped.
	for _, scopeName := range []string{"global", "tenant", "project"} {
		want := 0
		for _, permission := range Permissions() {
			if !InertAt(permission, scopeName) {
				want++
			}
		}
		if got := len(byScope[scopeName].Permissions); got != want {
			t.Errorf(
				"%s capability has %d permissions, want %d",
				scopeName,
				got,
				want,
			)
		}
	}

	// The three scopes must not all report the same thing, or the assertions
	// above would hold for a filter that does nothing.
	if len(byScope["global"].Permissions) <= len(byScope["tenant"].Permissions) ||
		len(byScope["tenant"].Permissions) <= len(byScope["project"].Permissions) {
		t.Errorf(
			"capability sizes global=%d tenant=%d project=%d are not strictly narrowing",
			len(byScope["global"].Permissions),
			len(byScope["tenant"].Permissions),
			len(byScope["project"].Permissions),
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
		TenantID:       testTenantID,
		ProjectID:      testProjectID,
		AgentNamespace: "zke-system",
	}, nil
}

func permissionNames(permissions []Permission) []string {
	names := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		names = append(names, string(permission))
	}
	return names
}

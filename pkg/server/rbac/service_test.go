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

	err = service.AuthorizeProject(
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
				return service.AuthorizeProject(
					context.Background(),
					testUserID,
					PermissionClusterRead,
					"not-a-uuid",
				)
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

func TestRoleAndScopeRules(t *testing.T) {
	t.Parallel()

	targetScope := projectScope(testTenantID, testProjectID)
	if !roleGrants("admin", PermissionAgentRevoke) {
		t.Fatal("admin role did not grant agent.revoke")
	}
	if !roleGrants("viewer", PermissionClusterRead) {
		t.Fatal("viewer role did not grant cluster.read")
	}
	if roleGrants("viewer", PermissionAgentEnrollmentCreate) {
		t.Fatal("viewer role granted agent.enrollment.create")
	}
	if !roleGrants("admin", PermissionTenantCreate) ||
		!roleGrants("admin", PermissionProjectCreate) {
		t.Fatal("admin role did not grant resource creation permissions")
	}
	if roleGrants("viewer", PermissionTenantCreate) ||
		roleGrants("viewer", PermissionProjectCreate) {
		t.Fatal("viewer role granted resource creation permissions")
	}
	for _, permission := range []Permission{
		PermissionUserRead,
		PermissionUserManage,
		PermissionRBACRead,
		PermissionRBACManage,
		PermissionAuditRead,
	} {
		if !roleGrants("admin", permission) {
			t.Errorf("admin role did not grant %s", permission)
		}
		if roleGrants("viewer", permission) {
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

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

func TestValidUUID(t *testing.T) {
	t.Parallel()

	if !validUUID("01234567-89ab-cdef-0123-456789abcdef") {
		t.Fatal("validUUID() rejected a UUID")
	}
	if validUUID("01234567-89ab-cdef-0123-456789abcdeg") {
		t.Fatal("validUUID() accepted a non-hex UUID")
	}
}

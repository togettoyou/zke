package rbac

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/togettoyou/zke/pkg/server/store"
)

const otherTenantID = "00000000-0000-0000-0000-000000000005"
const otherProjectID = "00000000-0000-0000-0000-000000000006"

// fakeStore serves fixed RoleBindings so the authorization rules can be tested
// without PostgreSQL. Authorization is the one place where an untested edge
// case turns directly into a privilege escalation.
type fakeStore struct {
	bindings     []store.RoleBinding
	projectOwner map[string]string
	clusterScope map[string]store.ClusterAuthorizationScope
	err          error
}

func (fake *fakeStore) ListRoleBindings(
	_ context.Context,
	_ string,
) ([]store.RoleBinding, error) {
	return fake.bindings, fake.err
}

func (fake *fakeStore) FindProjectTenant(
	_ context.Context,
	projectID string,
) (string, error) {
	if fake.err != nil {
		return "", fake.err
	}
	tenantID, exists := fake.projectOwner[projectID]
	if !exists {
		return "", store.ErrProjectNotFound
	}
	return tenantID, nil
}

func (fake *fakeStore) FindClusterAuthorizationScope(
	_ context.Context,
	clusterID string,
) (store.ClusterAuthorizationScope, error) {
	if fake.err != nil {
		return store.ClusterAuthorizationScope{}, fake.err
	}
	scope, exists := fake.clusterScope[clusterID]
	if !exists {
		return store.ClusterAuthorizationScope{}, store.ErrClusterNotFound
	}
	return scope, nil
}

func TestAuthorizeAppliesScopeBoundaries(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		bindings  []store.RoleBinding
		authorize func(*Service) error
		allowed   bool
	}{
		{
			name:     "global admin reaches a project",
			bindings: []store.RoleBinding{{Role: "admin", ScopeType: "global"}},
			authorize: func(service *Service) error {
				return service.AuthorizeProject(
					context.Background(), testUserID, PermissionProjectManage, testProjectID,
				)
			},
			allowed: true,
		},
		{
			name: "tenant admin reaches a project in their tenant",
			bindings: []store.RoleBinding{
				{Role: "admin", ScopeType: "tenant", TenantID: testTenantID},
			},
			authorize: func(service *Service) error {
				return service.AuthorizeProject(
					context.Background(), testUserID, PermissionProjectManage, testProjectID,
				)
			},
			allowed: true,
		},
		{
			name: "tenant admin cannot reach another tenant's project",
			bindings: []store.RoleBinding{
				{Role: "admin", ScopeType: "tenant", TenantID: otherTenantID},
			},
			authorize: func(service *Service) error {
				return service.AuthorizeProject(
					context.Background(), testUserID, PermissionProjectManage, testProjectID,
				)
			},
			allowed: false,
		},
		{
			// A project-scoped binding must not widen into tenant-level authority.
			name: "project admin cannot act at tenant scope",
			bindings: []store.RoleBinding{
				{
					Role: "admin", ScopeType: "project",
					TenantID: testTenantID, ProjectID: testProjectID,
				},
			},
			authorize: func(service *Service) error {
				return service.AuthorizeTenant(
					context.Background(), testUserID, PermissionProjectCreate, testTenantID,
				)
			},
			allowed: false,
		},
		{
			// Nor into global authority.
			name: "tenant admin cannot act at global scope",
			bindings: []store.RoleBinding{
				{Role: "admin", ScopeType: "tenant", TenantID: testTenantID},
			},
			authorize: func(service *Service) error {
				return service.AuthorizeGlobal(
					context.Background(), testUserID, PermissionUserManage,
				)
			},
			allowed: false,
		},
		{
			name: "viewer cannot revoke a Cluster connection",
			bindings: []store.RoleBinding{
				{Role: "viewer", ScopeType: "global"},
			},
			authorize: func(service *Service) error {
				_, err := service.AuthorizeCluster(
					context.Background(),
					testUserID,
					PermissionClusterConnectionRevoke,
					testClusterID,
				)
				return err
			},
			allowed: false,
		},
		{
			name: "viewer may read a Cluster",
			bindings: []store.RoleBinding{
				{Role: "viewer", ScopeType: "global"},
			},
			authorize: func(service *Service) error {
				_, err := service.AuthorizeCluster(
					context.Background(), testUserID, PermissionClusterRead, testClusterID,
				)
				return err
			},
			allowed: true,
		},
		{
			name:     "a user with no binding is denied",
			bindings: nil,
			authorize: func(service *Service) error {
				return service.AuthorizeGlobal(
					context.Background(), testUserID, PermissionTenantRead,
				)
			},
			allowed: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			service := NewService(&fakeStore{
				bindings:     testCase.bindings,
				projectOwner: map[string]string{testProjectID: testTenantID},
				clusterScope: map[string]store.ClusterAuthorizationScope{
					testClusterID: {TenantID: testTenantID, ProjectID: testProjectID},
				},
			})
			err := testCase.authorize(service)
			if testCase.allowed && err != nil {
				t.Fatalf("authorization error = %v, want nil", err)
			}
			if !testCase.allowed && !errors.Is(err, ErrDenied) {
				t.Fatalf("authorization error = %v, want ErrDenied", err)
			}
		})
	}
}

// An unknown Cluster or Project must be denied rather than reported as
// missing, so that probing cannot map resources outside the caller's scope.
func TestUnknownTargetsAreDeniedRatherThanDisclosed(t *testing.T) {
	t.Parallel()

	service := NewService(&fakeStore{
		bindings: []store.RoleBinding{{Role: "admin", ScopeType: "global"}},
	})
	if err := service.AuthorizeProject(
		context.Background(), testUserID, PermissionProjectRead, testProjectID,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("AuthorizeProject(unknown) error = %v, want ErrDenied", err)
	}
	if _, err := service.AuthorizeCluster(
		context.Background(), testUserID, PermissionClusterRead, testClusterID,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("AuthorizeCluster(unknown) error = %v, want ErrDenied", err)
	}
}

func TestResolveVisibility(t *testing.T) {
	t.Parallel()

	service := NewService(&fakeStore{bindings: []store.RoleBinding{
		{Role: "viewer", ScopeType: "tenant", TenantID: testTenantID},
		{
			Role: "viewer", ScopeType: "project",
			TenantID: otherTenantID, ProjectID: otherProjectID,
		},
		// An admin-only permission on this binding must not widen viewer reach.
		{Role: "viewer", ScopeType: "global"},
	}})

	visibility, err := service.ResolveVisibility(
		context.Background(), testUserID, PermissionTenantRead,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !visibility.IsGlobal() {
		t.Fatal("a global viewer binding must produce global visibility for tenant.read")
	}

	scoped := NewService(&fakeStore{bindings: []store.RoleBinding{
		{Role: "viewer", ScopeType: "tenant", TenantID: testTenantID},
		{
			Role: "viewer", ScopeType: "project",
			TenantID: otherTenantID, ProjectID: otherProjectID,
		},
	}})
	visibility, err = scoped.ResolveVisibility(
		context.Background(), testUserID, PermissionTenantRead,
	)
	if err != nil {
		t.Fatal(err)
	}
	if visibility.IsGlobal() {
		t.Fatal("scoped bindings must not produce global visibility")
	}
	if !visibility.HasAny() {
		t.Fatal("scoped bindings must produce some visibility")
	}
	if !slices.Contains(visibility.TenantIDs(), testTenantID) {
		t.Fatalf("tenant IDs = %v, want to contain %s", visibility.TenantIDs(), testTenantID)
	}
	if !slices.Contains(visibility.ProjectIDs(), otherProjectID) {
		t.Fatalf("project IDs = %v, want to contain %s", visibility.ProjectIDs(), otherProjectID)
	}
	// A project-scoped user must still see the tenant holding their project,
	// otherwise they could never navigate to it.
	if !slices.Contains(visibility.ProjectTenantIDs(), otherTenantID) {
		t.Fatalf(
			"project tenant IDs = %v, want to contain %s",
			visibility.ProjectTenantIDs(),
			otherTenantID,
		)
	}

	if !visibility.AllowsTenant(testTenantID) ||
		!visibility.AllowsTenant(otherTenantID) {
		t.Fatal("both the granted tenant and the project's tenant must be visible")
	}
	if visibility.AllowsTenant("00000000-0000-0000-0000-000000000009") {
		t.Fatal("an ungranted tenant must not be visible")
	}
	if !visibility.AllowsProject(testTenantID, testProjectID) {
		t.Fatal("a tenant-wide binding must cover its projects")
	}
	if visibility.AllowsProject(otherTenantID, testProjectID) {
		t.Fatal("a project binding must not cover a different project")
	}
}

// A permission the role does not grant must produce no visibility at all,
// rather than falling back to the bindings' scopes.
func TestResolveVisibilityIgnoresBindingsWithoutThePermission(t *testing.T) {
	t.Parallel()

	service := NewService(&fakeStore{bindings: []store.RoleBinding{
		{Role: "viewer", ScopeType: "tenant", TenantID: testTenantID},
	}})
	visibility, err := service.ResolveVisibility(
		context.Background(), testUserID, PermissionAuditRead,
	)
	if err != nil {
		t.Fatal(err)
	}
	if visibility.HasAny() {
		t.Fatal("a viewer must have no audit.read visibility")
	}
}

func TestListCapabilities(t *testing.T) {
	t.Parallel()

	service := NewService(&fakeStore{bindings: []store.RoleBinding{
		{Role: "viewer", ScopeType: "tenant", TenantID: testTenantID},
	}})
	capabilities, err := service.ListCapabilities(context.Background(), testUserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(capabilities) != 1 {
		t.Fatalf("capability count = %d, want 1", len(capabilities))
	}
	permissions := capabilities[0].Permissions
	if !slices.Contains(permissions, PermissionClusterRead) {
		t.Fatalf("viewer permissions = %v, want to contain cluster.read", permissions)
	}
	if slices.Contains(permissions, PermissionUserManage) {
		t.Fatalf("viewer permissions = %v, must not contain user.manage", permissions)
	}
	if !slices.IsSorted(permissions) {
		t.Fatalf("permissions are not sorted: %v", permissions)
	}
}

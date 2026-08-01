package rbac

import (
	"context"
	"testing"

	"github.com/togettoyou/zke/pkg/server/store"
)

// countingStore reports how often a binding lookup reached persistence.
type countingStore struct {
	fakeStore
	calls int
}

func (counting *countingStore) ListRoleBindings(
	ctx context.Context,
	userID string,
) ([]store.RoleBinding, error) {
	counting.calls++
	return counting.fakeStore.ListRoleBindings(ctx, userID)
}

func TestBindingCacheResolvesOncePerRequest(t *testing.T) {
	t.Parallel()

	counting := &countingStore{fakeStore: fakeStore{
		bindings: []store.RoleBinding{{Role: RoleAdmin, ScopeType: "global"}},
	}}
	service := NewService(counting)
	ctx := WithBindingCache(context.Background())

	if err := service.AuthorizeGlobal(ctx, testUserID, PermissionUserRead); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveVisibility(
		ctx, testUserID, PermissionTenantRead,
	); err != nil {
		t.Fatal(err)
	}
	if counting.calls != 1 {
		t.Fatalf("role binding lookups = %d, want 1", counting.calls)
	}
}

// Without a cache installed every check must reach persistence, so a
// long-lived caller cannot keep acting on withdrawn bindings.
func TestBindingLookupsAreNotSharedWithoutACache(t *testing.T) {
	t.Parallel()

	counting := &countingStore{fakeStore: fakeStore{
		bindings: []store.RoleBinding{{Role: RoleAdmin, ScopeType: "global"}},
	}}
	service := NewService(counting)
	ctx := context.Background()

	for range 2 {
		if err := service.AuthorizeGlobal(ctx, testUserID, PermissionUserRead); err != nil {
			t.Fatal(err)
		}
	}
	if counting.calls != 2 {
		t.Fatalf("role binding lookups = %d, want 2", counting.calls)
	}
}

func TestWithoutBindingCacheShadowsInheritedRequestCache(t *testing.T) {
	t.Parallel()

	counting := &countingStore{fakeStore: fakeStore{
		bindings: []store.RoleBinding{{Role: RoleAdmin, ScopeType: "global"}},
	}}
	service := NewService(counting)
	requestContext := WithBindingCache(context.Background())
	if err := service.AuthorizeGlobal(
		requestContext,
		testUserID,
		PermissionUserRead,
	); err != nil {
		t.Fatal(err)
	}
	streamContext := WithoutBindingCache(requestContext)
	for range 2 {
		if err := service.AuthorizeGlobal(
			streamContext,
			testUserID,
			PermissionUserRead,
		); err != nil {
			t.Fatal(err)
		}
	}
	if counting.calls != 3 {
		t.Fatalf("role binding lookups = %d, want 3", counting.calls)
	}
}

// Two requests must not see each other's bindings.
func TestBindingCacheIsScopedToOneContext(t *testing.T) {
	t.Parallel()

	counting := &countingStore{fakeStore: fakeStore{
		bindings: []store.RoleBinding{{Role: RoleAdmin, ScopeType: "global"}},
	}}
	service := NewService(counting)

	for range 2 {
		ctx := WithBindingCache(context.Background())
		if err := service.AuthorizeGlobal(ctx, testUserID, PermissionUserRead); err != nil {
			t.Fatal(err)
		}
	}
	if counting.calls != 2 {
		t.Fatalf("role binding lookups = %d, want 2", counting.calls)
	}
}

func TestRoleMatrixIsTheOnlySourceOfRoles(t *testing.T) {
	t.Parallel()

	for _, role := range Roles() {
		if !RoleExists(role) {
			t.Errorf("RoleExists(%q) = false for a listed role", role)
		}
	}
	for _, role := range []string{"", "Admin", "operator", "owner"} {
		if RoleExists(role) {
			t.Errorf("RoleExists(%q) = true", role)
		}
	}
	if !roleGrants(RoleAdmin, PermissionRBACManage) {
		t.Error("admin does not grant rbac.manage")
	}
	if roleGrants(RoleViewer, PermissionRBACManage) {
		t.Error("viewer grants rbac.manage")
	}
	if !roleGrants(RoleViewer, PermissionClusterRead) {
		t.Error("viewer does not grant cluster.read")
	}
}

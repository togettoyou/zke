package rbac

import (
	"context"
	"slices"
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

func TestBuiltinRolesCoverTheirDeclaredMeaning(t *testing.T) {
	t.Parallel()

	for _, name := range []string{RoleAdmin, RoleViewer} {
		if !IsBuiltinRole(name) {
			t.Errorf("IsBuiltinRole(%q) = false for a shipped role", name)
		}
	}
	for _, name := range []string{"", "Admin", "operator", "owner"} {
		if IsBuiltinRole(name) {
			t.Errorf("IsBuiltinRole(%q) = true", name)
		}
	}
	// admin is "every permission there is" rather than a list, so the check is
	// against the vocabulary: a permission added to the Server and missing here
	// would be a permission nobody could hold.
	admin := builtinRolePermissions(RoleAdmin)
	for _, permission := range Permissions() {
		if !slices.Contains(admin, string(permission)) {
			t.Errorf("admin does not grant %q", permission)
		}
	}
	viewer := builtinRolePermissions(RoleViewer)
	if slices.Contains(viewer, string(PermissionRBACManage)) {
		t.Error("viewer grants rbac.manage")
	}
	if !slices.Contains(viewer, string(PermissionClusterRead)) {
		t.Error("viewer does not grant cluster.read")
	}
}

// The store protects the account of last resort by role name, and names that
// role as a literal because it sits below this package. This is the check that
// keeps the literal honest: a global administrator is someone holding the
// builtin `admin` role, so the store and this package have to mean the same
// role by it.
func TestGlobalAdminRoleMatchesAuthorization(t *testing.T) {
	t.Parallel()

	if store.GlobalAdminRoleName() != RoleAdmin {
		t.Fatalf(
			"store.GlobalAdminRoleName() = %q, want %q",
			store.GlobalAdminRoleName(),
			RoleAdmin,
		)
	}
	if !IsBuiltinRole(store.GlobalAdminRoleName()) {
		t.Fatal("the global administrator role is not a builtin role")
	}
}

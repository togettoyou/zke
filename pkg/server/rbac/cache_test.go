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

// The self-lockout rule compares the actor's global permission set before and
// after a role update, so it names no permission at all and there is nothing
// here for it to drift against.
//
// What it does depend on is the ceiling being measured over global bindings —
// the same set — since that is what makes a loss unrecoverable and the refusal
// worth making. `GlobalPermissions` is that set, and this asserts it stays
// global-only.
func TestGlobalPermissionsIgnoresScopedBindings(t *testing.T) {
	t.Parallel()

	service := NewService(bindingStub{bindings: []store.RoleBinding{
		{
			Role:        "scoped",
			ScopeType:   "tenant",
			TenantID:    testTenantID,
			Permissions: permissionNames([]Permission{PermissionClusterRead}),
		},
		{
			Role:        "global",
			ScopeType:   "global",
			Permissions: permissionNames([]Permission{PermissionRBACManage}),
		},
	}})

	held, err := service.GlobalPermissions(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("GlobalPermissions() error = %v", err)
	}
	if _, granted := held[PermissionRBACManage]; !granted {
		t.Error("GlobalPermissions() dropped a permission held through a global binding")
	}
	if _, granted := held[PermissionClusterRead]; granted {
		t.Error("GlobalPermissions() counted a permission held only through a tenant binding")
	}
}

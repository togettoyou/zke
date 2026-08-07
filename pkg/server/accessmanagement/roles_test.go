package accessmanagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

const testRoleID = "00000000-0000-4000-8000-000000000005"

// fakeRoleStore serves roles without PostgreSQL and records what reached it.
type fakeRoleStore struct {
	Store

	role        store.ManagedRole
	created     store.CreateManagedRoleParams
	updated     store.UpdateManagedRoleParams
	createCalls int
	updateCalls int
	bindCalls   int
	err         error
	findErr     error
}

func (fake *fakeRoleStore) FindRoleByName(
	_ context.Context,
	_ string,
) (store.ManagedRole, error) {
	if fake.findErr != nil {
		return store.ManagedRole{}, fake.findErr
	}
	return fake.role, nil
}

func (fake *fakeRoleStore) CreateRole(
	_ context.Context,
	input store.CreateManagedRoleParams,
) (store.ManagedRole, error) {
	fake.createCalls++
	fake.created = input
	return store.ManagedRole{ID: input.ID, Name: input.Name}, fake.err
}

func (fake *fakeRoleStore) UpdateRole(
	_ context.Context,
	input store.UpdateManagedRoleParams,
) (store.ManagedRole, error) {
	fake.updateCalls++
	fake.updated = input
	return store.ManagedRole{ID: input.RoleID}, fake.err
}

func (fake *fakeRoleStore) CreateRoleBinding(
	_ context.Context,
	_ store.CreateManagedRoleBindingParams,
) (store.ManagedRoleBinding, bool, error) {
	fake.bindCalls++
	return store.ManagedRoleBinding{ID: testUserID}, false, fake.err
}

// fixedAuthority reports a caller's global permissions.
type fixedAuthority struct {
	held []rbac.Permission
	err  error
}

func (authority fixedAuthority) GlobalPermissions(
	_ context.Context,
	_ string,
) (map[rbac.Permission]struct{}, error) {
	if authority.err != nil {
		return nil, authority.err
	}
	result := make(map[rbac.Permission]struct{}, len(authority.held))
	for _, permission := range authority.held {
		result[permission] = struct{}{}
	}
	return result, nil
}

func roleService(
	roleStore *fakeRoleStore,
	held ...rbac.Permission,
) *Service {
	return NewService(roleStore, Config{MaxConcurrentPasswordHashes: 1}).
		WithPermissionAuthority(fixedAuthority{held: held})
}

func createRoleInput(permissions ...string) CreateRoleInput {
	return CreateRoleInput{
		Name:        "release-engineer",
		DisplayName: "发布工程师",
		Permissions: permissions,
		Confirm:     true,
		ActorUserID: testUserID,
		RequestID:   "00000000-0000-4000-8000-00000000000a",
		Now:         time.Now().UTC(),
	}
}

// The check the whole role model rests on.
//
// Without it `rbac.manage` is the only permission anyone needs: write a role
// carrying whatever you like, bind it to yourself, and every other permission
// the platform defines becomes reachable. These cases are the reason the ceiling
// exists, so they assert the refusal reaches the caller *and* that nothing was
// written on the way.
func TestCreateRoleRefusesPermissionsTheActorDoesNotHold(t *testing.T) {
	t.Parallel()

	fake := &fakeRoleStore{}
	service := roleService(fake, rbac.PermissionClusterRead)
	_, err := service.CreateRole(
		context.Background(),
		createRoleInput(
			string(rbac.PermissionClusterRead),
			string(rbac.PermissionClusterSecretRead),
		),
	)
	if !errors.Is(err, ErrPermissionEscalation) {
		t.Fatalf("CreateRole() error = %v, want ErrPermissionEscalation", err)
	}
	if fake.createCalls != 0 {
		t.Fatal("a refused role reached the store")
	}
}

func TestCreateRoleAcceptsPermissionsWithinTheCeiling(t *testing.T) {
	t.Parallel()

	fake := &fakeRoleStore{}
	service := roleService(
		fake, rbac.PermissionClusterRead, rbac.PermissionClusterSecretRead,
	)
	if _, err := service.CreateRole(
		context.Background(),
		createRoleInput(
			string(rbac.PermissionClusterSecretRead),
			string(rbac.PermissionClusterRead),
		),
	); err != nil {
		t.Fatalf("CreateRole() error = %v, want nil", err)
	}
	// Stored in a stable order so that two roles with the same permissions read
	// the same way in the Console and in an audit trail.
	want := []string{
		string(rbac.PermissionClusterRead),
		string(rbac.PermissionClusterSecretRead),
	}
	if len(fake.created.Permissions) != len(want) {
		t.Fatalf("stored permissions = %v, want %v", fake.created.Permissions, want)
	}
	for index, permission := range want {
		if fake.created.Permissions[index] != permission {
			t.Fatalf("stored permissions = %v, want %v", fake.created.Permissions, want)
		}
	}
}

// The shape the store's authority refusals actually have: a sentinel to match
// on and the permission names to report. Mirrored here rather than exported
// from the store, which has no other reason to publish the type.
type detailedStoreError struct {
	target error
	detail string
}

func (err *detailedStoreError) Error() string {
	return err.target.Error() + ": " + err.detail
}

func (err *detailedStoreError) Is(target error) bool {
	return target == err.target
}

func (err *detailedStoreError) Detail() string {
	return err.detail
}

// An update's authority check belongs to the store, and its answer has to
// arrive here still carrying the permission names.
//
// The check itself moved because an update is judged on what it *changes*, and
// the stored set it changes has to be read under the same lock as the write.
// What this asserts is the seam: the store's two refusals become this package's
// sentinels, and the names survive the relabelling. Dropping them would leave an
// operator told that a list of checkboxes is wrong without being told which one.
// The rule itself is exercised against a real database in
// `TestUpdateRoleChecksBothDirectionsAgainstTheActor`.
func TestUpdateRoleReportsTheStoresAuthorityRefusals(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		from  error
		want  error
		names string
	}{
		{
			name:  "adding",
			from:  store.ErrRoleGrantForbidden,
			want:  ErrPermissionEscalation,
			names: "rbac.manage",
		},
		{
			name:  "removing",
			from:  store.ErrRoleRevokeForbidden,
			want:  ErrPermissionRevocation,
			names: "tenant.create, tenant.manage",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRoleStore{
				err: &detailedStoreError{target: testCase.from, detail: testCase.names},
			}
			service := roleService(fake, rbac.PermissionClusterRead)
			_, err := service.UpdateRole(context.Background(), UpdateRoleInput{
				RoleID:      testRoleID,
				DisplayName: "发布工程师",
				Permissions: []string{string(rbac.PermissionClusterRead)},
				Confirm:     true,
				ActorUserID: testUserID,
				RequestID:   "00000000-0000-4000-8000-00000000000a",
				Now:         time.Now().UTC(),
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("UpdateRole() error = %v, want %v", err, testCase.want)
			}
			var detailed interface{ Detail() string }
			if !errors.As(err, &detailed) || detailed.Detail() != testCase.names {
				t.Fatalf("refusal detail = %v, want %q", err, testCase.names)
			}
		})
	}
}

// Binding is the shorter way round the same wall.
//
// `admin` already exists and already carries everything, so checking only the
// authoring path would leave anyone with `rbac.manage` able to bind it to
// themselves. This is that case.
func TestCreateRoleBindingRefusesARoleBeyondTheActorsCeiling(t *testing.T) {
	t.Parallel()

	fake := &fakeRoleStore{
		role: store.ManagedRole{
			ID:          testRoleID,
			Name:        "admin",
			Permissions: []string{string(rbac.PermissionClusterSecretRead)},
		},
	}
	service := roleService(fake, rbac.PermissionRBACManage)
	_, err := service.CreateRoleBinding(context.Background(), CreateRoleBindingInput{
		SubjectID:   testUserID,
		Role:        "admin",
		ScopeType:   "global",
		ActorUserID: testUserID,
		RequestID:   "00000000-0000-4000-8000-00000000000a",
		Now:         time.Now().UTC(),
	})
	if !errors.Is(err, ErrPermissionEscalation) {
		t.Fatalf("CreateRoleBinding() error = %v, want ErrPermissionEscalation", err)
	}
	if fake.bindCalls != 0 {
		t.Fatal("a refused binding reached the store")
	}
}

func TestCreateRoleBindingReportsAMissingRole(t *testing.T) {
	t.Parallel()

	fake := &fakeRoleStore{findErr: store.ErrRoleNotFound}
	service := roleService(fake, rbac.PermissionRBACManage)
	_, err := service.CreateRoleBinding(context.Background(), CreateRoleBindingInput{
		SubjectID:   testUserID,
		Role:        "does-not-exist",
		ScopeType:   "global",
		ActorUserID: testUserID,
		RequestID:   "00000000-0000-4000-8000-00000000000a",
		Now:         time.Now().UTC(),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateRoleBinding() error = %v, want ErrNotFound", err)
	}
}

// A missing authority must read as a refusal rather than as an absent
// restriction: it is the only thing standing between rbac.manage and every
// other permission, and a nil dependency is not a reason to hand them out.
func TestRoleWritesRefuseWithoutAPermissionAuthority(t *testing.T) {
	t.Parallel()

	service := NewService(&fakeRoleStore{}, Config{MaxConcurrentPasswordHashes: 1})
	if _, err := service.CreateRole(
		context.Background(),
		createRoleInput(string(rbac.PermissionClusterRead)),
	); err == nil {
		t.Fatal("CreateRole() succeeded without a permission authority")
	}
}

func TestCreateRoleRejectsUnknownAndBuiltinNames(t *testing.T) {
	t.Parallel()

	service := roleService(&fakeRoleStore{}, rbac.Permissions()...)
	unknownPermission := createRoleInput("cluster.secret.steal")
	if _, err := service.CreateRole(
		context.Background(), unknownPermission,
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateRole() error = %v, want ErrInvalidInput", err)
	}
	builtin := createRoleInput(string(rbac.PermissionClusterRead))
	builtin.Name = rbac.RoleAdmin
	if _, err := service.CreateRole(
		context.Background(), builtin,
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateRole(admin) error = %v, want ErrInvalidInput", err)
	}
	empty := createRoleInput()
	if _, err := service.CreateRole(
		context.Background(), empty,
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateRole(no permissions) error = %v, want ErrInvalidInput", err)
	}
}

// A binding that would grant nothing is refused rather than written.
//
// Every permission in this role is only ever checked at global scope, so on a
// Tenant or Project binding it authorizes nothing while reading as an
// authorization. The refusal names them, because the operator's next move is a
// role that fits the scope and they cannot build one without knowing which
// permissions did not fit.
func TestCreateRoleBindingRefusesARoleThatGrantsNothingAtItsScope(t *testing.T) {
	t.Parallel()

	for _, scope := range []struct {
		name      string
		scopeType string
		projectID string
	}{
		{name: "tenant", scopeType: "tenant"},
		{name: "project", scopeType: "project", projectID: testProjectID},
	} {
		t.Run(scope.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRoleStore{
				role: store.ManagedRole{
					ID:   testRoleID,
					Name: "platform-operator",
					Permissions: []string{
						string(rbac.PermissionUserManage),
						string(rbac.PermissionRBACRead),
					},
				},
			}
			service := roleService(
				fake,
				rbac.PermissionRBACManage,
				rbac.PermissionRBACRead,
				rbac.PermissionUserManage,
			)
			_, err := service.CreateRoleBinding(context.Background(), CreateRoleBindingInput{
				SubjectID:   testUserID,
				Role:        "platform-operator",
				ScopeType:   scope.scopeType,
				TenantID:    testTenantID,
				ProjectID:   scope.projectID,
				ActorUserID: testUserID,
				RequestID:   "00000000-0000-4000-8000-00000000000a",
				Now:         time.Now().UTC(),
			})
			if !errors.Is(err, ErrRoleUnreachableAtScope) {
				t.Fatalf("CreateRoleBinding() error = %v, want ErrRoleUnreachableAtScope", err)
			}
			var detailed interface{ Detail() string }
			if !errors.As(err, &detailed) ||
				detailed.Detail() != string(rbac.PermissionRBACRead)+", "+
					string(rbac.PermissionUserManage) {
				t.Fatalf("refusal did not name the permissions: %v", err)
			}
			if fake.bindCalls != 0 {
				t.Fatal("a refused binding reached the store")
			}
		})
	}
}

// `project.create` is checked with `RequireTenant`, so a Project binding of a
// role made only of it grants exactly nothing — and a Tenant binding of the same
// role is a real grant.
//
// The two halves have to be asserted together. While the rule was a boolean
// "global only", the first half silently passed: the permission is not global,
// so nothing refused a binding that reached nothing.
func TestCreateRoleBindingRefusesTenantFloorPermissionAtProjectScope(t *testing.T) {
	t.Parallel()

	for _, scope := range []struct {
		name      string
		scopeType string
		projectID string
		refused   bool
	}{
		{name: "project", scopeType: "project", projectID: testProjectID, refused: true},
		{name: "tenant", scopeType: "tenant"},
	} {
		t.Run(scope.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRoleStore{
				role: store.ManagedRole{
					ID:          testRoleID,
					Name:        "project-founder",
					Permissions: []string{string(rbac.PermissionProjectCreate)},
				},
			}
			service := roleService(
				fake,
				rbac.PermissionRBACManage,
				rbac.PermissionProjectCreate,
			)
			_, err := service.CreateRoleBinding(context.Background(), CreateRoleBindingInput{
				SubjectID:   testUserID,
				Role:        "project-founder",
				ScopeType:   scope.scopeType,
				TenantID:    testTenantID,
				ProjectID:   scope.projectID,
				ActorUserID: testUserID,
				RequestID:   "00000000-0000-4000-8000-00000000000a",
				Now:         time.Now().UTC(),
			})
			if scope.refused {
				if !errors.Is(err, ErrRoleUnreachableAtScope) {
					t.Fatalf("CreateRoleBinding() error = %v, want ErrRoleUnreachableAtScope", err)
				}
				if fake.bindCalls != 0 {
					t.Fatal("a refused binding reached the store")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateRoleBinding() error = %v, want the tenant binding accepted", err)
			}
		})
	}
}

// A role that reaches anything at the scope is accepted, including one that
// also carries permissions the scope cannot exercise.
//
// This is the case the rule must not break: `admin` scoped to a Tenant is a
// real grant — most of it applies there — and refusing it would leave no way to
// say "everything here" without hand-building a Tenant-shaped copy of the
// builtin role.
func TestCreateRoleBindingAcceptsAPartiallyReachableRole(t *testing.T) {
	t.Parallel()

	mixed := &fakeRoleStore{
		role: store.ManagedRole{
			ID:   testRoleID,
			Name: "admin",
			Permissions: []string{
				string(rbac.PermissionProjectRead),
				string(rbac.PermissionUserManage),
			},
		},
	}
	service := roleService(
		mixed,
		rbac.PermissionRBACManage,
		rbac.PermissionProjectRead,
		rbac.PermissionUserManage,
	)
	if _, err := service.CreateRoleBinding(context.Background(), CreateRoleBindingInput{
		SubjectID:   testUserID,
		Role:        "admin",
		ScopeType:   "tenant",
		TenantID:    testTenantID,
		ActorUserID: testUserID,
		RequestID:   "00000000-0000-4000-8000-00000000000a",
		Now:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRoleBinding(partially reachable) error = %v, want nil", err)
	}
	if mixed.bindCalls != 1 {
		t.Fatal("an accepted binding did not reach the store")
	}

	// The same all-global role is fine at global scope: the rule is about what
	// this scope reaches, not about the role.
	global := &fakeRoleStore{
		role: store.ManagedRole{
			ID:          testRoleID,
			Name:        "platform-operator",
			Permissions: []string{string(rbac.PermissionUserManage)},
		},
	}
	globalService := roleService(global, rbac.PermissionRBACManage, rbac.PermissionUserManage)
	if _, err := globalService.CreateRoleBinding(context.Background(), CreateRoleBindingInput{
		SubjectID:   testUserID,
		Role:        "platform-operator",
		ScopeType:   "global",
		ActorUserID: testUserID,
		RequestID:   "00000000-0000-4000-8000-00000000000a",
		Now:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRoleBinding(global) error = %v, want nil", err)
	}
}

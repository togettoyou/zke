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

func TestUpdateRoleRefusesPermissionsTheActorDoesNotHold(t *testing.T) {
	t.Parallel()

	fake := &fakeRoleStore{}
	service := roleService(fake, rbac.PermissionClusterRead)
	_, err := service.UpdateRole(context.Background(), UpdateRoleInput{
		RoleID:      testRoleID,
		DisplayName: "发布工程师",
		Permissions: []string{string(rbac.PermissionRBACManage)},
		Confirm:     true,
		ActorUserID: testUserID,
		RequestID:   "00000000-0000-4000-8000-00000000000a",
		Now:         time.Now().UTC(),
	})
	if !errors.Is(err, ErrPermissionEscalation) {
		t.Fatalf("UpdateRole() error = %v, want ErrPermissionEscalation", err)
	}
	if fake.updateCalls != 0 {
		t.Fatal("a refused role edit reached the store")
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

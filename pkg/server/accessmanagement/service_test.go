package accessmanagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

const (
	testActorID   = "00000000-0000-4000-8000-000000000001"
	testUserID    = "00000000-0000-4000-8000-000000000002"
	testTenantID  = "00000000-0000-4000-8000-000000000003"
	testProjectID = "00000000-0000-4000-8000-000000000004"
	testRequestID = "request-0001"
)

// fakeStore records what the service asked for and returns canned results, so
// the service's own rules can be exercised without PostgreSQL.
type fakeStore struct {
	Store

	listUsersParams store.ListManagedUsersParams
	users           []store.ManagedUser
	usersTotal      int

	listBindingsParams store.ListManagedRoleBindingsParams
	bindings           []store.ManagedRoleBinding
	bindingsTotal      int

	deleteUserCalled bool
	err              error
}

func (fake *fakeStore) ListUsers(
	_ context.Context,
	params store.ListManagedUsersParams,
) ([]store.ManagedUser, int, error) {
	fake.listUsersParams = params
	return fake.users, fake.usersTotal, fake.err
}

func (fake *fakeStore) ListRoleBindings(
	_ context.Context,
	params store.ListManagedRoleBindingsParams,
) ([]store.ManagedRoleBinding, int, error) {
	fake.listBindingsParams = params
	return fake.bindings, fake.bindingsTotal, fake.err
}

func (fake *fakeStore) DeleteUser(
	_ context.Context,
	_ store.DeleteManagedUserParams,
) (store.ManagedUser, error) {
	fake.deleteUserCalled = true
	return store.ManagedUser{ID: testUserID}, fake.err
}

func (fake *fakeStore) SetUserStatus(
	_ context.Context,
	_ store.SetManagedUserStatusParams,
) (store.ManagedUser, error) {
	return store.ManagedUser{ID: testUserID}, fake.err
}

func (fake *fakeStore) CreateRoleBinding(
	_ context.Context,
	_ store.CreateManagedRoleBindingParams,
) (store.ManagedRoleBinding, bool, error) {
	return store.ManagedRoleBinding{ID: testUserID}, false, fake.err
}

func newTestService(fake *fakeStore) *Service {
	return NewService(fake, Config{MaxConcurrentPasswordHashes: 1})
}

func validPage() pagination.Request {
	return pagination.Request{Limit: pagination.DefaultLimit}
}

func TestListUsersPassesFiltersToTheStore(t *testing.T) {
	t.Parallel()

	fake := &fakeStore{
		users:      []store.ManagedUser{{ID: testUserID, Username: "operator"}},
		usersTotal: 7,
	}
	result, err := newTestService(fake).ListUsers(
		context.Background(),
		ListUsersInput{
			Status: "active",
			Search: "  OPERATOR  ",
			Page:   pagination.Request{Limit: 2, Offset: 4},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// The search term must reach the store lowercased and trimmed, because the
	// store compares it against already-lowercased columns.
	if fake.listUsersParams.Search != "operator" {
		t.Fatalf("store search = %q, want %q", fake.listUsersParams.Search, "operator")
	}
	if fake.listUsersParams.Status != "active" ||
		fake.listUsersParams.Page.Limit != 2 ||
		fake.listUsersParams.Page.Offset != 4 {
		t.Fatalf("unexpected store params: %+v", fake.listUsersParams)
	}
	// The reported total describes the filtered set, not the returned page.
	if result.Page.Total != 7 || result.Page.Limit != 2 || result.Page.Offset != 4 {
		t.Fatalf("unexpected page result: %+v", result.Page)
	}
	if !result.Page.HasMore {
		t.Fatal("a page ending before the total must report more results")
	}
	if len(result.Users) != 1 || result.Users[0].Username != "operator" {
		t.Fatalf("unexpected users: %+v", result.Users)
	}
}

func TestListUsersRejectsUnusableRequests(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		input ListUsersInput
	}{
		{name: "unknown status", input: ListUsersInput{Status: "archived", Page: validPage()}},
		{name: "zero page size", input: ListUsersInput{Page: pagination.Request{Limit: 0}}},
		{
			name:  "page size above the maximum",
			input: ListUsersInput{Page: pagination.Request{Limit: pagination.MaxLimit + 1}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// A nil store proves the request never reached persistence.
			service := NewService(nil, Config{})
			if _, err := service.ListUsers(
				context.Background(),
				testCase.input,
			); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ListUsers() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestListRoleBindingsRejectsUnknownEnums(t *testing.T) {
	t.Parallel()

	service := NewService(nil, Config{})
	for _, input := range []ListRoleBindingsInput{
		{Role: "superuser", Page: validPage()},
		{ScopeType: "namespace", Page: validPage()},
	} {
		if _, err := service.ListRoleBindings(
			context.Background(),
			input,
		); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ListRoleBindings(%+v) error = %v, want ErrInvalidInput", input, err)
		}
	}
}

func TestListRoleBindingsPassesFiltersToTheStore(t *testing.T) {
	t.Parallel()

	fake := &fakeStore{bindingsTotal: 3}
	result, err := newTestService(fake).ListRoleBindings(
		context.Background(),
		ListRoleBindingsInput{
			Role:      "viewer",
			ScopeType: "project",
			Search:    "Alpha",
			Page:      validPage(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if fake.listBindingsParams.Role != "viewer" ||
		fake.listBindingsParams.ScopeType != "project" ||
		fake.listBindingsParams.Search != "alpha" {
		t.Fatalf("unexpected store params: %+v", fake.listBindingsParams)
	}
	if result.Page.Total != 3 {
		t.Fatalf("page total = %d, want 3", result.Page.Total)
	}
}

// An administrator must not be able to delete or disable their own account:
// doing so could strand the platform with no usable operator session.
func TestSelfTargetedRemovalIsRefusedBeforeReachingTheStore(t *testing.T) {
	t.Parallel()

	fake := &fakeStore{}
	service := newTestService(fake)
	now := time.Now().UTC()

	if _, err := service.DeleteUser(context.Background(), DeleteUserInput{
		UserID:      testActorID,
		Confirm:     true,
		ActorUserID: testActorID,
		RequestID:   testRequestID,
		Now:         now,
	}); !errors.Is(err, ErrSelfDisable) {
		t.Fatalf("DeleteUser(self) error = %v, want ErrSelfDisable", err)
	}
	if fake.deleteUserCalled {
		t.Fatal("a self-targeted deletion must not reach the store")
	}

	if _, err := service.SetUserStatus(context.Background(), SetUserStatusInput{
		UserID:      testActorID,
		Status:      "disabled",
		ActorUserID: testActorID,
		RequestID:   testRequestID,
		Now:         now,
	}); !errors.Is(err, ErrSelfDisable) {
		t.Fatalf("SetUserStatus(self, disabled) error = %v, want ErrSelfDisable", err)
	}
}

// Deleting another account is allowed; only self-targeting is refused.
func TestDeleteUserReachesTheStoreForAnotherAccount(t *testing.T) {
	t.Parallel()

	fake := &fakeStore{}
	if _, err := newTestService(fake).DeleteUser(
		context.Background(),
		DeleteUserInput{
			UserID:      testUserID,
			Confirm:     true,
			ActorUserID: testActorID,
			RequestID:   testRequestID,
			Now:         time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	if !fake.deleteUserCalled {
		t.Fatal("deleting another account must reach the store")
	}
}

// Every destructive operation requires explicit confirmation, and an
// unconfirmed request must never reach persistence.
func TestUnconfirmedDestructiveOperationsAreRefused(t *testing.T) {
	t.Parallel()

	fake := &fakeStore{}
	service := newTestService(fake)
	now := time.Now().UTC()

	if _, err := service.DeleteUser(context.Background(), DeleteUserInput{
		UserID:      testUserID,
		Confirm:     false,
		ActorUserID: testActorID,
		RequestID:   testRequestID,
		Now:         now,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DeleteUser(unconfirmed) error = %v, want ErrInvalidInput", err)
	}
	if fake.deleteUserCalled {
		t.Fatal("an unconfirmed deletion must not reach the store")
	}

	if _, err := service.DeleteRoleBinding(
		context.Background(),
		DeleteRoleBindingInput{
			BindingID:   testUserID,
			Confirm:     false,
			ActorUserID: testActorID,
			RequestID:   testRequestID,
			Now:         now,
		},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DeleteRoleBinding(unconfirmed) error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateRoleBindingRejectsInconsistentScopes(t *testing.T) {
	t.Parallel()

	service := NewService(nil, Config{})
	now := time.Now().UTC()
	base := CreateRoleBindingInput{
		SubjectID:   testUserID,
		Role:        "admin",
		ActorUserID: testActorID,
		RequestID:   testRequestID,
		Now:         now,
	}

	for _, testCase := range []struct {
		name  string
		mutit func(*CreateRoleBindingInput)
	}{
		{
			name: "global scope carrying a tenant",
			mutit: func(input *CreateRoleBindingInput) {
				input.ScopeType = "global"
				input.TenantID = testTenantID
			},
		},
		{
			name: "tenant scope carrying a project",
			mutit: func(input *CreateRoleBindingInput) {
				input.ScopeType = "tenant"
				input.TenantID = testTenantID
				input.ProjectID = testProjectID
			},
		},
		{
			name: "project scope without its tenant",
			mutit: func(input *CreateRoleBindingInput) {
				input.ScopeType = "project"
				input.ProjectID = testProjectID
			},
		},
		{
			name: "unknown role",
			mutit: func(input *CreateRoleBindingInput) {
				input.ScopeType = "global"
				input.Role = "superuser"
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := base
			testCase.mutit(&input)
			if _, err := service.CreateRoleBinding(
				context.Background(),
				input,
			); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateRoleBinding() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestStoreErrorsMapToServiceErrors(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		storeErr error
		expected error
	}{
		{"missing user", store.ErrAccessUserNotFound, ErrNotFound},
		{"state conflict", store.ErrAccessStateConflict, ErrConflict},
		{"last global administrator", store.ErrLastGlobalAdmin, ErrLastAdmin},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeStore{err: testCase.storeErr}
			_, err := newTestService(fake).SetUserStatus(
				context.Background(),
				SetUserStatusInput{
					UserID:      testUserID,
					Status:      "disabled",
					ActorUserID: testActorID,
					RequestID:   testRequestID,
					Now:         time.Now().UTC(),
				},
			)
			if !errors.Is(err, testCase.expected) {
				t.Fatalf("SetUserStatus() error = %v, want %v", err, testCase.expected)
			}
		})
	}
}

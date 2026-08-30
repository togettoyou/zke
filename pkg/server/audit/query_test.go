package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

const (
	queryUserID   = "00000000-0000-4000-8000-000000000001"
	queryTenantID = "00000000-0000-4000-8000-000000000002"
)

type fakeAuditStore struct {
	Store

	params  store.ListAuditRecordsParams
	records []store.AuditRecord
	total   int
	err     error
}

func (fake *fakeAuditStore) ListRecords(
	_ context.Context,
	params store.ListAuditRecordsParams,
) ([]store.AuditRecord, int, error) {
	fake.params = params
	return fake.records, fake.total, fake.err
}

type fakeRBACStore struct {
	bindings []store.RoleBinding
}

// ListRoleBindings resolves each binding's permission set the way the real
// query does, by joining the role. Cases below name a builtin role and leave
// the permissions empty, so what a role means stays defined in one place.
func (fake *fakeRBACStore) ListRoleBindings(
	_ context.Context,
	_ string,
) ([]store.RoleBinding, error) {
	resolved := make([]store.RoleBinding, 0, len(fake.bindings))
	for _, binding := range fake.bindings {
		if binding.Permissions == nil {
			binding.Permissions = builtinRolePermissions(binding.Role)
		}
		resolved = append(resolved, binding)
	}
	return resolved, nil
}

func builtinRolePermissions(name string) []string {
	for _, role := range rbac.BuiltinRoles() {
		if role.Name != name {
			continue
		}
		permissions := make([]string, 0, len(role.Permissions))
		for _, permission := range role.Permissions {
			permissions = append(permissions, string(permission))
		}
		return permissions
	}
	return nil
}

func (fake *fakeRBACStore) FindProjectTenant(
	_ context.Context,
	_ string,
) (string, error) {
	return "", store.ErrProjectNotFound
}

func (fake *fakeRBACStore) FindClusterAuthorizationScope(
	_ context.Context,
	_ string,
) (store.ClusterAuthorizationScope, error) {
	return store.ClusterAuthorizationScope{}, store.ErrClusterNotFound
}

func newQueryService(
	auditStore Store,
	bindings []store.RoleBinding,
) *Service {
	return NewService(auditStore, rbac.NewService(&fakeRBACStore{bindings: bindings}))
}

// A caller without audit.read anywhere must be denied outright rather than
// receiving an empty page, which would hide the authorization failure.
func TestQueryDeniesCallersWithoutAuditVisibility(t *testing.T) {
	t.Parallel()

	fake := &fakeAuditStore{}
	service := newQueryService(fake, []store.RoleBinding{
		{Role: "viewer", ScopeType: "global"},
	})
	_, err := service.Query(context.Background(), QueryInput{
		UserID: queryUserID,
		Page:   pagination.Request{Limit: pagination.DefaultLimit},
	})
	if !errors.Is(err, rbac.ErrDenied) {
		t.Fatalf("Query() error = %v, want rbac.ErrDenied", err)
	}
	if fake.params.Page.Limit != 0 {
		t.Fatal("a denied query must not reach the store")
	}
}

// Visibility must be pushed into the query, otherwise the total would count
// events the caller may not see.
func TestQueryPushesVisibilityIntoTheStore(t *testing.T) {
	t.Parallel()

	fake := &fakeAuditStore{total: 42}
	service := newQueryService(fake, []store.RoleBinding{
		{Role: "admin", ScopeType: "tenant", TenantID: queryTenantID},
	})
	result, err := service.Query(context.Background(), QueryInput{
		UserID: queryUserID,
		Page:   pagination.Request{Limit: 10, Offset: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.params.GlobalVisible {
		t.Fatal("a tenant-scoped caller must not query with global visibility")
	}
	if len(fake.params.TenantIDs) != 1 || fake.params.TenantIDs[0] != queryTenantID {
		t.Fatalf("store tenant IDs = %v, want [%s]", fake.params.TenantIDs, queryTenantID)
	}
	if fake.params.Page.Limit != 10 || fake.params.Page.Offset != 20 {
		t.Fatalf("unexpected store page: %+v", fake.params.Page)
	}
	if result.Page.Total != 42 {
		t.Fatalf("page total = %d, want 42", result.Page.Total)
	}
}

func TestQueryPassesInternalChangeCorrelationFilters(t *testing.T) {
	t.Parallel()

	fake := &fakeAuditStore{}
	service := newQueryService(fake, []store.RoleBinding{
		{Role: "admin", ScopeType: "global"},
	})
	since := time.Now().UTC().Add(-time.Hour)
	_, err := service.Query(context.Background(), QueryInput{
		UserID: queryUserID, ClusterID: "00000000-0000-4000-8000-000000000003",
		Actions: []string{auditaction.KubernetesResourcePatch}, Since: since,
		DetailContains: map[string]string{"mutating": "true"},
		Page:           pagination.Request{Limit: pagination.DefaultLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.params.Actions) != 1 || fake.params.Actions[0] != auditaction.KubernetesResourcePatch ||
		!fake.params.Since.Equal(since) || fake.params.DetailContains["mutating"] != "true" {
		t.Fatalf("store params = %+v", fake.params)
	}
	// Query owns its copy: a caller changing its map after return must not be
	// able to alter a store request retained for logging or tests.
	inputDetail := map[string]string{"mutating": "true"}
	_, err = service.Query(context.Background(), QueryInput{
		UserID: queryUserID, DetailContains: inputDetail,
		Page: pagination.Request{Limit: pagination.DefaultLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputDetail["mutating"] = "false"
	if fake.params.DetailContains["mutating"] != "true" {
		t.Fatalf("detail filter was not cloned: %+v", fake.params.DetailContains)
	}
}

func TestQueryRejectsUnusableRequests(t *testing.T) {
	t.Parallel()

	service := newQueryService(&fakeAuditStore{}, []store.RoleBinding{
		{Role: "admin", ScopeType: "global"},
	})
	validPage := pagination.Request{Limit: pagination.DefaultLimit}

	for _, testCase := range []struct {
		name  string
		input QueryInput
	}{
		{"invalid user", QueryInput{UserID: "not-a-uuid", Page: validPage}},
		{"zero page size", QueryInput{UserID: queryUserID, Page: pagination.Request{Limit: 0}}},
		{
			"page size above the maximum",
			QueryInput{
				UserID: queryUserID,
				Page:   pagination.Request{Limit: pagination.MaxLimit + 1},
			},
		},
		{
			"offset beyond the supported depth",
			QueryInput{
				UserID: queryUserID,
				Page:   pagination.Request{Limit: 10, Offset: pagination.MaxOffset + 1},
			},
		},
		{"unknown actor type", QueryInput{UserID: queryUserID, ActorType: "robot", Page: validPage}},
		{"unknown result", QueryInput{UserID: queryUserID, Result: "maybe", Page: validPage}},
		{"invalid tenant filter", QueryInput{UserID: queryUserID, TenantID: "nope", Page: validPage}},
		{"invalid project filter", QueryInput{UserID: queryUserID, ProjectID: "nope", Page: validPage}},
		{"invalid cluster filter", QueryInput{UserID: queryUserID, ClusterID: "nope", Page: validPage}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := service.Query(
				context.Background(),
				testCase.input,
			); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("Query() error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func TestQueryMapsRecordsToEvents(t *testing.T) {
	t.Parallel()

	createdAt := time.Now().UTC().Truncate(time.Second)
	fake := &fakeAuditStore{
		total: 1,
		records: []store.AuditRecord{{
			ID:          "00000000-0000-4000-8000-00000000000a",
			ActorType:   "user",
			ActorUserID: queryUserID,
			ScopeType:   "tenant",
			TenantID:    queryTenantID,
			Action:      "tenant.update",
			TargetType:  "tenant",
			Result:      "succeeded",
			RequestID:   "request-0001",
			CreatedAt:   createdAt,
		}},
	}
	service := newQueryService(fake, []store.RoleBinding{
		{Role: "admin", ScopeType: "global"},
	})
	result, err := service.Query(context.Background(), QueryInput{
		UserID: queryUserID,
		Page:   pagination.Request{Limit: pagination.DefaultLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("event count = %d, want 1", len(result.Events))
	}
	event := result.Events[0]
	if event.Action != "tenant.update" ||
		event.TenantID != queryTenantID ||
		event.Result != "succeeded" ||
		!event.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected event: %+v", event)
	}
	if result.Page.HasMore {
		t.Fatal("a complete result set must not report more pages")
	}
}

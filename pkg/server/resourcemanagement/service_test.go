package resourcemanagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/shared/pagination"
)

func TestServiceRejectsInvalidInputBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, nil)
	validPage := pagination.Request{Limit: pagination.DefaultLimit}
	if _, err := service.ListTenants(
		context.Background(),
		ListTenantsInput{UserID: "not-a-uuid", Page: validPage},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTenants() error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.ListProjects(
		context.Background(),
		ListProjectsInput{
			UserID:   "00000000-0000-4000-8000-000000000001",
			TenantID: "not-a-uuid",
			Page:     validPage,
		},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListProjects() error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.CreateTenant(
		context.Background(),
		CreateTenantInput{
			Name:           " tenant ",
			ActorUserID:    "00000000-0000-4000-8000-000000000001",
			RequestID:      "request",
			IdempotencyKey: "resource-create-0001",
			Now:            time.Now().UTC(),
		},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateTenant() error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.CreateProject(
		context.Background(),
		CreateProjectInput{
			TenantID:       "00000000-0000-4000-8000-000000000002",
			Name:           "",
			ActorUserID:    "00000000-0000-4000-8000-000000000001",
			RequestID:      "request",
			IdempotencyKey: "resource-create-0001",
			Now:            time.Now().UTC(),
		},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateProject() error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.UpdateCluster(
		context.Background(),
		UpdateClusterInput{
			ClusterID:   "not-a-uuid",
			Name:        "cluster",
			ActorUserID: "00000000-0000-4000-8000-000000000001",
			RequestID:   "request",
			Now:         time.Now().UTC(),
		},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateCluster() error = %v, want ErrInvalidInput", err)
	}
}

// A list request must be rejected before it reaches the store when its page
// bounds or enum filters are unusable, so an invalid page can never turn into
// an unbounded query.
func TestServiceRejectsInvalidListRequests(t *testing.T) {
	t.Parallel()

	service := NewService(nil, nil)
	userID := "00000000-0000-4000-8000-000000000001"
	tenantID := "00000000-0000-4000-8000-000000000002"
	validPage := pagination.Request{Limit: pagination.DefaultLimit}

	for _, testCase := range []struct {
		name  string
		input ListTenantsInput
	}{
		{
			name:  "zero page size",
			input: ListTenantsInput{UserID: userID, Page: pagination.Request{Limit: 0}},
		},
		{
			name: "page size above the maximum",
			input: ListTenantsInput{
				UserID: userID,
				Page:   pagination.Request{Limit: pagination.MaxLimit + 1},
			},
		},
		{
			name: "offset beyond the supported depth",
			input: ListTenantsInput{
				UserID: userID,
				Page:   pagination.Request{Limit: 10, Offset: pagination.MaxOffset + 1},
			},
		},
		{
			name:  "unknown status filter",
			input: ListTenantsInput{UserID: userID, Status: "archived", Page: validPage},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := service.ListTenants(
				context.Background(),
				testCase.input,
			); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ListTenants() error = %v, want ErrInvalidInput", err)
			}
		})
	}

	if _, err := service.ListProjects(
		context.Background(),
		ListProjectsInput{
			UserID:   userID,
			TenantID: tenantID,
			Status:   "archived",
			Page:     validPage,
		},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListProjects() error = %v, want ErrInvalidInput", err)
	}
}

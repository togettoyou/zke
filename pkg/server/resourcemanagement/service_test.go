package resourcemanagement

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceRejectsInvalidInputBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, nil)
	if _, err := service.ListTenants(
		context.Background(),
		"not-a-uuid",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ListTenants() error = %v, want ErrInvalidInput", err)
	}
	if _, err := service.ListProjects(
		context.Background(),
		"00000000-0000-4000-8000-000000000001",
		"not-a-uuid",
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
	if _, err := service.GetCluster(
		context.Background(),
		"not-a-uuid",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GetCluster() error = %v, want ErrInvalidInput", err)
	}
}

package agentstatus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

const (
	listProjectID = "00000000-0000-4000-8000-000000000001"
	listClusterID = "00000000-0000-4000-8000-000000000002"
	listAgentID   = "00000000-0000-4000-8000-000000000003"
)

type fakeStatusStore struct {
	Store

	params store.ListProjectAgentCertificatesParams
	items  []store.ProjectAgentCertificate
	total  int
	err    error
}

func (fake *fakeStatusStore) ListProjectAgentCertificates(
	_ context.Context,
	params store.ListProjectAgentCertificatesParams,
) ([]store.ProjectAgentCertificate, int, error) {
	fake.params = params
	return fake.items, fake.total, fake.err
}

// fakeConnections reports live connection state without a QUIC listener.
type fakeConnections struct {
	statuses map[string]agentconn.ConnectionStatus
}

func (fake *fakeConnections) Snapshot(
	agentIDs []string,
) map[string]agentconn.ConnectionStatus {
	result := make(map[string]agentconn.ConnectionStatus, len(agentIDs))
	for _, agentID := range agentIDs {
		if status, exists := fake.statuses[agentID]; exists {
			result[agentID] = status
		}
	}
	return result
}

func TestListProjectPassesFiltersAndMergesConnectionState(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	connectedAt := now.Add(-time.Hour)
	fake := &fakeStatusStore{
		total: 5,
		items: []store.ProjectAgentCertificate{{
			ProjectID:            listProjectID,
			ClusterID:            listClusterID,
			ClusterName:          "cluster-a",
			ClusterStatus:        "active",
			AgentID:              listAgentID,
			LifecycleStatus:      "active",
			HealthStatus:         "healthy",
			CertificateExpiresAt: now.Add(90 * 24 * time.Hour),
		}},
	}
	service := NewService(
		fake,
		&fakeConnections{statuses: map[string]agentconn.ConnectionStatus{
			listAgentID: {
				State:        agentconn.ConnectionStateOnline,
				ConnectionID: "connection-1",
				ConnectedAt:  connectedAt,
			},
		}},
		nil,
		30*24*time.Hour,
	)

	result, err := service.ListProject(context.Background(), ListProjectInput{
		ProjectID: listProjectID,
		Status:    "active",
		Search:    "  Cluster-A  ",
		Now:       now,
		Page:      pagination.Request{Limit: 2, Offset: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.params.Status != "active" || fake.params.Search != "cluster-a" {
		t.Fatalf("unexpected store params: %+v", fake.params)
	}
	if fake.params.Page.Limit != 2 {
		t.Fatalf("store page limit = %d, want 2", fake.params.Page.Limit)
	}
	if result.Page.Total != 5 || !result.Page.HasMore {
		t.Fatalf("unexpected page: %+v", result.Page)
	}
	if len(result.Agents) != 1 {
		t.Fatalf("agent count = %d, want 1", len(result.Agents))
	}
	agent := result.Agents[0]
	if agent.ConnectionStatus != agentconn.ConnectionStateOnline ||
		agent.ConnectionID != "connection-1" ||
		agent.ConnectedAt == nil ||
		!agent.ConnectedAt.Equal(connectedAt) {
		t.Fatalf("live connection state was not merged: %+v", agent)
	}
	if agent.CertificateStatus != "valid" {
		t.Fatalf("certificate status = %q, want valid", agent.CertificateStatus)
	}
}

// A Cluster with no live connection must still be listed, reported offline.
func TestListProjectReportsDisconnectedClusters(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	fake := &fakeStatusStore{
		total: 1,
		items: []store.ProjectAgentCertificate{{
			ClusterID:            listClusterID,
			AgentID:              listAgentID,
			ClusterStatus:        "pending",
			CertificateExpiresAt: now.Add(time.Hour),
		}},
	}
	service := NewService(fake, &fakeConnections{}, nil, 30*24*time.Hour)
	result, err := service.ListProject(context.Background(), ListProjectInput{
		ProjectID: listProjectID,
		Now:       now,
		Page:      pagination.Request{Limit: pagination.DefaultLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Agents) != 1 {
		t.Fatalf("agent count = %d, want 1", len(result.Agents))
	}
	if result.Agents[0].ConnectionStatus != agentconn.ConnectionStateOffline {
		t.Fatalf("connection status = %q, want offline", result.Agents[0].ConnectionStatus)
	}
	// A certificate inside the warning window must be reported as expiring.
	if result.Agents[0].CertificateStatus != "expiring" {
		t.Fatalf("certificate status = %q, want expiring", result.Agents[0].CertificateStatus)
	}
}

func TestListProjectRejectsUnusableRequests(t *testing.T) {
	t.Parallel()

	service := NewService(nil, nil, nil, time.Hour)
	now := time.Now().UTC()
	validPage := pagination.Request{Limit: pagination.DefaultLimit}

	for _, testCase := range []struct {
		name  string
		input ListProjectInput
	}{
		{"invalid project", ListProjectInput{ProjectID: "nope", Now: now, Page: validPage}},
		{"zero time", ListProjectInput{ProjectID: listProjectID, Page: validPage}},
		{
			"zero page size",
			ListProjectInput{
				ProjectID: listProjectID,
				Now:       now,
				Page:      pagination.Request{Limit: 0},
			},
		},
		{
			"unknown status",
			ListProjectInput{
				ProjectID: listProjectID,
				Status:    "archived",
				Now:       now,
				Page:      validPage,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := service.ListProject(
				context.Background(),
				testCase.input,
			); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ListProject() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

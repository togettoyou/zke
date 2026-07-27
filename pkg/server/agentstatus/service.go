package agentstatus

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/pagination"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput      = errors.New("invalid Agent status input")
	ErrNotFound          = store.ErrAgentNotFound
	ErrEventsUnavailable = errors.New("Agent status events unavailable")
)

type Service struct {
	store         Store
	connections   ConnectionStatusSource
	warningBefore time.Duration
}

type ConnectionStatusSource interface {
	Snapshot(agentIDs []string) map[string]agentconn.ConnectionStatus
}

type ConnectionEventSource interface {
	Subscribe() (<-chan agentconn.ConnectionEvent, func())
}

type Agent struct {
	TenantID                    string
	ProjectID                   string
	ClusterID                   string
	ClusterName                 string
	ClusterStatus               string
	ClusterCreatedAt            time.Time
	ClusterUpdatedAt            time.Time
	AgentID                     string
	AgentVersion                string
	ProtocolVersion             string
	LifecycleStatus             string
	HealthStatus                string
	LastSeenAt                  *time.Time
	CertificateSerial           string
	CertificateExpiresAt        time.Time
	CertificateRemainingSeconds int64
	CertificateStatus           string
	ConnectionStatus            string
	ConnectionID                string
	ConnectedAt                 *time.Time
	LastHeartbeatAt             *time.Time
	LastDisconnectedAt          *time.Time
	LastDisconnectReason        string
}

func (service *Service) Subscribe() (<-chan agentconn.ConnectionEvent, func(), error) {
	source, ok := service.connections.(ConnectionEventSource)
	if !ok || source == nil {
		return nil, nil, ErrEventsUnavailable
	}
	events, unsubscribe := source.Subscribe()
	return events, unsubscribe, nil
}

func NewService(
	agentStore Store,
	connections ConnectionStatusSource,
	warningBefore time.Duration,
) *Service {
	return &Service{
		store:         agentStore,
		connections:   connections,
		warningBefore: warningBefore,
	}
}

// ListProject returns one page of a project's Clusters with their Agent
// status. Only the persisted Cluster attributes are filtered in SQL; live
// connection state is merged in afterwards from the connection manager.
func (service *Service) ListProject(
	ctx context.Context,
	input ListProjectInput,
) (AgentPage, error) {
	if !validation.IsUUID(input.ProjectID) ||
		input.Now.IsZero() ||
		input.Page.Validate() != nil ||
		!allowedValue(input.Status, "pending", "active", "revoked") {
		return AgentPage{}, ErrInvalidInput
	}
	stored, total, err := service.store.ListProjectAgentCertificates(
		ctx,
		store.ListProjectAgentCertificatesParams{
			ProjectID: input.ProjectID,
			Status:    input.Status,
			Search:    strings.ToLower(strings.TrimSpace(input.Search)),
			Page:      input.Page,
		},
	)
	if err != nil {
		return AgentPage{}, err
	}
	agents := service.buildAgents(stored, input.Now)
	return AgentPage{
		Agents: agents,
		Page:   pagination.NewResult(input.Page, total, len(agents)),
	}, nil
}

// ListProjectInput selects one page of a project's Clusters.
type ListProjectInput struct {
	ProjectID string
	Status    string
	Search    string
	Now       time.Time
	Page      pagination.Request
}

// AgentPage is one page of Cluster status plus its position.
type AgentPage struct {
	Agents []Agent
	Page   pagination.Result
}

func allowedValue(value string, candidates ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func (service *Service) GetCluster(
	ctx context.Context,
	clusterID string,
	now time.Time,
) (Agent, error) {
	if !validation.IsUUID(clusterID) || now.IsZero() {
		return Agent{}, ErrInvalidInput
	}
	stored, err := service.store.GetClusterAgentCertificate(ctx, clusterID)
	if err != nil {
		return Agent{}, err
	}
	result := service.buildAgents([]store.ProjectAgentCertificate{stored}, now)
	if len(result) == 0 {
		return Agent{}, ErrNotFound
	}
	return result[0], nil
}

func (service *Service) buildAgents(
	stored []store.ProjectAgentCertificate,
	now time.Time,
) []Agent {
	agentIDs := make([]string, 0, len(stored))
	for _, item := range stored {
		agentIDs = append(agentIDs, item.AgentID)
	}
	connectionStatuses := make(map[string]agentconn.ConnectionStatus)
	if service.connections != nil {
		connectionStatuses = service.connections.Snapshot(agentIDs)
	}
	result := make([]Agent, 0, len(stored))
	for _, item := range stored {
		remaining := item.CertificateExpiresAt.Sub(now)
		status := certificateStatus(
			item.LifecycleStatus,
			item.CertificateRevokedAt,
			remaining,
			service.warningBefore,
		)
		remainingSeconds := int64(remaining / time.Second)
		if remainingSeconds < 0 {
			remainingSeconds = 0
		}
		agent := Agent{
			TenantID:                    item.TenantID,
			ProjectID:                   item.ProjectID,
			ClusterID:                   item.ClusterID,
			ClusterName:                 item.ClusterName,
			ClusterStatus:               item.ClusterStatus,
			ClusterCreatedAt:            item.ClusterCreatedAt,
			ClusterUpdatedAt:            item.ClusterUpdatedAt,
			AgentID:                     item.AgentID,
			AgentVersion:                item.AgentVersion,
			ProtocolVersion:             item.ProtocolVersion,
			LifecycleStatus:             item.LifecycleStatus,
			HealthStatus:                item.HealthStatus,
			LastSeenAt:                  item.LastSeenAt,
			CertificateSerial:           item.CertificateSerial,
			CertificateExpiresAt:        item.CertificateExpiresAt,
			CertificateRemainingSeconds: remainingSeconds,
			CertificateStatus:           status,
			ConnectionStatus:            agentconn.ConnectionStateOffline,
		}
		if connection, exists := connectionStatuses[item.AgentID]; exists {
			if connection.State == agentconn.ConnectionStateOnline {
				agent.ConnectionStatus = agentconn.ConnectionStateOnline
			}
			agent.ConnectionID = connection.ConnectionID
			agent.ConnectedAt = timePointer(connection.ConnectedAt)
			agent.LastHeartbeatAt = timePointer(connection.LastHeartbeatAt)
			agent.LastDisconnectedAt = timePointer(
				connection.LastDisconnectedAt,
			)
			agent.LastDisconnectReason = connection.LastDisconnectReason
		}
		result = append(result, agent)
	}
	return result
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func certificateStatus(
	lifecycleStatus string,
	revokedAt *time.Time,
	remaining time.Duration,
	warningBefore time.Duration,
) string {
	switch {
	case revokedAt != nil || lifecycleStatus == "revoked":
		return "revoked"
	case remaining <= 0:
		return "expired"
	case remaining <= warningBefore:
		return "expiring"
	default:
		return "valid"
	}
}

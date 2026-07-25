package agentstatus

import (
	"context"
	"errors"
	"time"

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var ErrInvalidInput = errors.New("invalid Agent status input")

type Service struct {
	store         *store.AgentStatusStore
	connections   ConnectionStatusSource
	warningBefore time.Duration
}

type ConnectionStatusSource interface {
	Snapshot(agentIDs []string) map[string]agentconn.ConnectionStatus
}

type Agent struct {
	ClusterID                   string
	ClusterName                 string
	AgentID                     string
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

func NewService(
	agentStore *store.AgentStatusStore,
	connections ConnectionStatusSource,
	warningBefore time.Duration,
) *Service {
	return &Service{
		store:         agentStore,
		connections:   connections,
		warningBefore: warningBefore,
	}
}

func (service *Service) ListProject(
	ctx context.Context,
	projectID string,
	now time.Time,
) ([]Agent, error) {
	if !validation.IsUUID(projectID) || now.IsZero() {
		return nil, ErrInvalidInput
	}
	stored, err := service.store.ListProjectAgentCertificates(ctx, projectID)
	if err != nil {
		return nil, err
	}
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
			ClusterID:                   item.ClusterID,
			ClusterName:                 item.ClusterName,
			AgentID:                     item.AgentID,
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
	return result, nil
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

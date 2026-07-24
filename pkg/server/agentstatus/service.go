package agentstatus

import (
	"context"
	"errors"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var ErrInvalidInput = errors.New("invalid Agent status input")

type Service struct {
	store         *store.AgentStatusStore
	warningBefore time.Duration
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
}

func NewService(
	agentStore *store.AgentStatusStore,
	warningBefore time.Duration,
) *Service {
	return &Service{store: agentStore, warningBefore: warningBefore}
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
		result = append(result, Agent{
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
		})
	}
	return result, nil
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

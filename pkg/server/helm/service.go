package helm

import (
	"context"
	"errors"
	"io"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

// AgentAccess is the Agent connection surface a release operation needs. The
// Server sends the chart and the values; the Agent runs Helm.
type AgentAccess interface {
	RequestHelm(
		ctx context.Context,
		clusterID string,
		request *agentv1.HelmRequest,
		values io.Reader,
		chart io.Reader,
		report io.Writer,
		idempotencyKey string,
	) (*agentv1.HelmResponse, error)
}

type Service struct {
	repositories RepositoryStore
	agents       AgentAccess
	catalogue    *indexCache
	charts       *chartCache
	userAgent    string
}

func NewService(
	repositories RepositoryStore,
	agents AgentAccess,
	userAgent string,
) (*Service, error) {
	if repositories == nil || agents == nil {
		return nil, errors.New("Helm service dependencies are required")
	}
	if userAgent == "" {
		userAgent = "zke-server"
	}
	return &Service{
		repositories: repositories,
		agents:       agents,
		catalogue:    newIndexCache(),
		charts:       newChartCache(),
		userAgent:    userAgent,
	}, nil
}

// forgetRepository drops everything this Server derived from one repository:
// its index and every chart read out of it. Both are answers to "what does
// this address publish", and a repository that was edited, disabled or removed
// no longer answers it the same way.
func (service *Service) forgetRepository(repositoryID string) {
	service.catalogue.forget(repositoryID)
	service.charts.forget(repositoryID)
}

func isUUID(value string) bool {
	return validation.IsUUID(value)
}

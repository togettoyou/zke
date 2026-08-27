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
		userAgent:    userAgent,
	}, nil
}

func isUUID(value string) bool {
	return validation.IsUUID(value)
}

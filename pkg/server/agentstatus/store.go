package agentstatus

import (
	"context"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface Cluster status reporting needs.
type Store interface {
	ListProjectAgentCertificates(ctx context.Context, params store.ListProjectAgentCertificatesParams) ([]store.ProjectAgentCertificate, int, error)
	GetClusterAgentCertificate(ctx context.Context, clusterID string) (store.ProjectAgentCertificate, error)
}

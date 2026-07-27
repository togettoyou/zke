package enrollment

import (
	"context"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface Cluster enrollment needs.
type Store interface {
	BeginAgentEnrollment(ctx context.Context, input store.BeginAgentEnrollmentParams) (store.AgentEnrollmentAttempt, error)
	CompleteAgentEnrollment(ctx context.Context, input store.CompleteAgentEnrollmentParams) (store.AgentEnrollmentResult, error)
	RecordAgentEnrollmentFailure(ctx context.Context, enrollmentID string, requestID string) error
	FindActiveEnrollmentByTokenDigest(ctx context.Context, tokenDigest []byte, now time.Time) (store.ActiveEnrollment, error)
	CreateEnrollment(ctx context.Context, input store.CreateEnrollmentParams) (store.Enrollment, error)
	GetClusterEnrollmentTarget(ctx context.Context, clusterID string) (store.ClusterEnrollmentTarget, error)
	ListEnrollments(ctx context.Context, params store.ListEnrollmentsParams) ([]store.Enrollment, int, error)
	GetEnrollment(ctx context.Context, projectID string, enrollmentID string) (store.Enrollment, error)
	RevokeEnrollment(ctx context.Context, params store.RevokeEnrollmentParams) (store.Enrollment, error)
}

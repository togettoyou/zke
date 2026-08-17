package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

var (
	ErrEnrollmentCreationDenied      = errors.New("enrollment creation denied")
	ErrEnrollmentIdempotencyConflict = errors.New("enrollment idempotency conflict")
	ErrEnrollmentTokenRejected       = errors.New("enrollment token rejected")
	ErrEnrollmentAttemptConflict     = errors.New("enrollment attempt conflict")
	ErrEnrollmentAttemptFailed       = errors.New("enrollment attempt failed")
	ErrEnrollmentAttemptNotFound     = errors.New("enrollment attempt not found")
	ErrEnrollmentNotFound            = errors.New("enrollment not found")
	ErrEnrollmentStateConflict       = errors.New("enrollment state conflict")
)

type EnrollmentStore struct {
	pool *pgxpool.Pool
}

// ListEnrollmentsParams filters and pages one project's Cluster enrollments.
// Now is passed in rather than read from the database clock so that the
// derived expiry status matches the timestamp the caller reports.
type ListEnrollmentsParams struct {
	ProjectID string
	Status    string
	Search    string
	Now       time.Time
	Page      pagination.Request
}

type Enrollment struct {
	ID                      string
	TenantID                string
	ProjectID               string
	ClusterID               string
	ClusterName             string
	CreatedByUserID         string
	ExpiresAt               time.Time
	ConsumedAt              *time.Time
	RevokedAt               *time.Time
	CreatedAt               time.Time
	EndpointProfileID       string
	EndpointProfileRevision int64
	AgentNamespace          string
}

type ActiveEnrollment struct {
	ID          string
	TenantID    string
	ProjectID   string
	ClusterID   string
	ClusterName string
	ExpiresAt   time.Time
	Snapshot    EnrollmentConfigurationSnapshot
}

type EnrollmentConfigurationSnapshot struct {
	EndpointProfileID            string
	EndpointProfileRevision      int64
	RegistrationURL              string
	QUICAddress                  string
	RegistrationCACertificatePEM string
	// AgentWorkload is the Agent's platform workload settings frozen at issue
	// time. It is the settings type rather than a copy of its fields, so a
	// field added to a workload reaches the installer without a change here.
	AgentWorkload  WorkloadSettings
	AgentNamespace string
}

type CreateEnrollmentParams struct {
	ProjectID       string
	ClusterID       string
	ClusterName     string
	CreatedByUserID string
	TokenDigest     []byte
	ExpiresAt       time.Time
	RequestID       string
	IdempotencyKey  string
	Snapshot        EnrollmentConfigurationSnapshot
}

type RevokeEnrollmentParams struct {
	ProjectID    string
	EnrollmentID string
	ActorUserID  string
	RequestID    string
	Now          time.Time
}

type ClusterEnrollmentTarget struct {
	ProjectID      string
	ClusterName    string
	AgentNamespace string
}

type EnrollmentAttemptStatus string

const (
	EnrollmentAttemptPending   EnrollmentAttemptStatus = "pending"
	EnrollmentAttemptSucceeded EnrollmentAttemptStatus = "succeeded"
	EnrollmentAttemptFailed    EnrollmentAttemptStatus = "failed"
)

type AgentEnrollmentResult struct {
	ClusterID            string    `json:"cluster_id"`
	AgentID              string    `json:"agent_id"`
	CertificatePEM       string    `json:"certificate_pem"`
	CertificateExpiresAt time.Time `json:"certificate_expires_at"`
}

type AgentEnrollmentAttempt struct {
	ID             string
	EnrollmentID   string
	TenantID       string
	ProjectID      string
	ClusterID      string
	ClusterName    string
	IdempotencyKey string
	CSRFingerprint []byte
	Status         EnrollmentAttemptStatus
	Result         *AgentEnrollmentResult
}

type BeginAgentEnrollmentParams struct {
	TokenDigest    []byte
	IdempotencyKey string
	CSRFingerprint []byte
	RequestID      string
	Now            time.Time
}

type CompleteAgentEnrollmentParams struct {
	EnrollmentID         string
	AttemptID            string
	IdempotencyKey       string
	CSRFingerprint       []byte
	ClusterID            string
	AgentID              string
	AgentVersion         string
	ProtocolVersion      string
	CertificateSerial    string
	CertificatePEM       string
	CertificateExpiresAt time.Time
	RequestID            string
	Now                  time.Time
}

func NewEnrollmentStore(pool *pgxpool.Pool) *EnrollmentStore {
	return &EnrollmentStore{pool: pool}
}

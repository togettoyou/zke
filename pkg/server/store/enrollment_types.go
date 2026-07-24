package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrEnrollmentCreationDenied      = errors.New("enrollment creation denied")
	ErrEnrollmentIdempotencyConflict = errors.New("enrollment idempotency conflict")
	ErrEnrollmentTokenRejected       = errors.New("enrollment token rejected")
	ErrEnrollmentAttemptConflict     = errors.New("enrollment attempt conflict")
	ErrEnrollmentAttemptFailed       = errors.New("enrollment attempt failed")
	ErrEnrollmentAttemptNotFound     = errors.New("enrollment attempt not found")
)

type EnrollmentStore struct {
	pool *pgxpool.Pool
}

type Enrollment struct {
	ID              string
	TenantID        string
	ProjectID       string
	CreatedByUserID string
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

type CreateEnrollmentParams struct {
	ProjectID       string
	CreatedByUserID string
	TokenDigest     []byte
	ExpiresAt       time.Time
	RequestID       string
	IdempotencyKey  string
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
	ClusterName          string
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

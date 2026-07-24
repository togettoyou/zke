package enrollment

import (
	"errors"
	"time"
)

const DefaultTokenTTL = 15 * time.Minute

var (
	ErrDenied              = errors.New("enrollment creation denied")
	ErrIdempotencyConflict = errors.New("enrollment idempotency conflict")
	ErrInvalidInput        = errors.New("invalid enrollment input")
	ErrTokenRejected       = errors.New("enrollment token rejected")
	ErrAttemptConflict     = errors.New("enrollment attempt conflict")
	ErrAttemptFailed       = errors.New("enrollment attempt failed")
	ErrSigningUnavailable  = errors.New("agent certificate signing unavailable")
)

type CreateInput struct {
	ProjectID      string
	ClusterName    string
	UserID         string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateResult struct {
	ID          string
	ClusterName string
	Token       string
	ExpiresAt   time.Time
}

type AttemptStatus string

const (
	AttemptPending   AttemptStatus = "pending"
	AttemptSucceeded AttemptStatus = "succeeded"
)

type AgentEnrollmentResult struct {
	ClusterID            string
	AgentID              string
	CertificatePEM       string
	CertificateExpiresAt time.Time
}

type BeginInput struct {
	Token          string
	IdempotencyKey string
	CSRPEM         []byte
	RequestID      string
	Now            time.Time
}

type BeginResult struct {
	AttemptID      string
	EnrollmentID   string
	TenantID       string
	ProjectID      string
	ClusterName    string
	IdempotencyKey string
	CSRFingerprint []byte
	Status         AttemptStatus
	Result         *AgentEnrollmentResult
}

type CompleteInput struct {
	EnrollmentID    string
	AttemptID       string
	IdempotencyKey  string
	CSRPEM          []byte
	ClusterID       string
	AgentID         string
	AgentVersion    string
	ProtocolVersion string
	CertificatePEM  string
	RequestID       string
	Now             time.Time
}

type EnrollInput struct {
	Token           string
	IdempotencyKey  string
	CSRPEM          []byte
	AgentVersion    string
	ProtocolVersion string
	RequestID       string
	Now             time.Time
}

type EnrollResult struct {
	AgentEnrollmentResult
	Replayed bool
}

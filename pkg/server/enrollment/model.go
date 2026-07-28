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
	ErrCredentialRejected  = errors.New("agent credential rejected")
	ErrScopeSuspended      = errors.New("agent scope suspended")
	ErrNotFound            = errors.New("enrollment not found")
	ErrStateConflict       = errors.New("enrollment state conflict")
	// ErrClusterNameConflict is raised both when the enrollment is issued and
	// when an Agent completes one: the name is free at the first point and can
	// be taken by the time the Agent arrives.
	ErrClusterNameConflict = errors.New("cluster name already exists in project")
)

type CreateInput struct {
	ProjectID      string
	ClusterID      string
	ClusterName    string
	UserID         string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateResult struct {
	ID          string
	ClusterID   string
	ClusterName string
	Token       string
	ExpiresAt   time.Time
}

type Enrollment struct {
	ID              string
	TenantID        string
	ProjectID       string
	ClusterID       string
	ClusterName     string
	CreatedByUserID string
	Status          string
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
	RevokedAt       *time.Time
	CreatedAt       time.Time
}

type RevokeInput struct {
	ProjectID    string
	EnrollmentID string
	Confirm      bool
	UserID       string
	RequestID    string
	Now          time.Time
}

type ReenrollInput struct {
	ClusterID      string
	UserID         string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type ManifestEnrollment struct {
	ID          string
	TenantID    string
	ProjectID   string
	ClusterName string
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
	ClusterID      string
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

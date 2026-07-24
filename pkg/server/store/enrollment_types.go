package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrEnrollmentCreationDenied      = errors.New("enrollment creation denied")
	ErrEnrollmentIdempotencyConflict = errors.New("enrollment idempotency conflict")
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

func NewEnrollmentStore(pool *pgxpool.Pool) *EnrollmentStore {
	return &EnrollmentStore{pool: pool}
}

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
)

type CreateInput struct {
	ProjectID      string
	UserID         string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateResult struct {
	ID        string
	Token     string
	ExpiresAt time.Time
}

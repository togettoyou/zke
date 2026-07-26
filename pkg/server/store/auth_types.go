package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInitialAdminExists = errors.New("initial administrator already exists")
	ErrCredentialsChanged = errors.New("user credentials changed")
	ErrUserNotFound       = errors.New("user not found")
	ErrSessionNotFound    = errors.New("session not found")
)

type AuthStore struct {
	pool *pgxpool.Pool
}

type User struct {
	ID                 string
	UsernameNormalized string
	DisplayName        string
	PasswordHash       string
	Status             string
	FailedLoginCount   int
	LockedAt           *time.Time
	LockExpiresAt      *time.Time
	PasswordChangedAt  time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type InitialAdmin struct {
	UsernameNormalized string
	DisplayName        string
	PasswordHash       string
	RequestID          string
}

type Session struct {
	ID              string
	UserID          string
	TokenDigest     []byte
	CSRFTokenDigest []byte
	IdleExpiresAt   time.Time
	ExpiresAt       time.Time
	LastSeenAt      time.Time
	RevokedAt       *time.Time
	CreatedAt       time.Time
}

type CreateSessionParams struct {
	UserID          string
	TokenDigest     []byte
	CSRFTokenDigest []byte
	IdleExpiresAt   time.Time
	ExpiresAt       time.Time
}

type CompleteLoginParams struct {
	UserID                    string
	ExpectedPasswordHash      string
	ExpectedPasswordChangedAt time.Time
	ReplacementPasswordHash   string
	Session                   CreateSessionParams
	RequestID                 string
	Now                       time.Time
}

type RecordLoginFailureParams struct {
	UserID       *string
	RequestID    string
	Now          time.Time
	MaxFailures  int
	LockDuration time.Duration
}

type AuthenticatedSession struct {
	Session Session
	User    SessionUser
}

type SessionUser struct {
	ID                 string
	UsernameNormalized string
	DisplayName        string
	Status             string
	PasswordChangedAt  time.Time
}

func NewAuthStore(pool *pgxpool.Pool) *AuthStore {
	return &AuthStore{pool: pool}
}

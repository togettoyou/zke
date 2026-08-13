package auth

import (
	"context"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface authentication needs.
type Store interface {
	HasGlobalAdministrator(ctx context.Context) (bool, error)
	CreateFirstGlobalAdministrator(ctx context.Context, input store.FirstGlobalAdministrator) (store.User, error)
	FindUserByUsername(ctx context.Context, usernameNormalized string) (store.User, error)
	FindUserByID(ctx context.Context, userID string) (store.User, error)
	CompleteLogin(ctx context.Context, input store.CompleteLoginParams) (store.Session, error)
	FindActiveSession(ctx context.Context, tokenDigest []byte, now time.Time, idleTimeout time.Duration) (store.AuthenticatedSession, error)
	RevokeAuthenticatedSession(ctx context.Context, sessionID string, userID string, revokedAt time.Time, requestID string) error
	ChangeOwnPassword(ctx context.Context, input store.ChangeOwnPasswordParams) error
	RecordLoginAudit(ctx context.Context, targetUserID *string, result string, requestID string) error
	RecordLoginFailure(ctx context.Context, input store.RecordLoginFailureParams) error
	RecordPasswordChangeAudit(ctx context.Context, userID string, result string, requestID string, now time.Time) error
}

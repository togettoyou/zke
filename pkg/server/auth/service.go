package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/auditctx"
)

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"

// securityWriteTimeout bounds the audit and lockout writes that outlive the
// request. It is a constant rather than configuration because it is not a
// tuning knob: these are single-row writes, and the only question is how long
// to wait for a database that has stopped answering.
const securityWriteTimeout = 5 * time.Second

var ErrInvalidCredentials = errors.New("invalid username or password")

var ErrUnauthenticated = errors.New("authentication required")

var ErrInvalidNewPassword = errors.New("new password does not satisfy password policy")

var ErrPasswordUnchanged = errors.New("new password must differ from current password")

type ServiceConfig struct {
	SessionIdleTimeout          time.Duration
	SessionAbsoluteTimeout      time.Duration
	MaxConcurrentPasswordChecks int
	MaxFailedLoginAttempts      int
	AccountLockDuration         time.Duration
}

type Service struct {
	store            Store
	config           ServiceConfig
	passwordParams   PasswordParams
	passwordChecks   chan struct{}
	passwordVerifier func([]byte, string) (bool, bool, error)
}

type LoginInput struct {
	Username  string
	Password  []byte
	RequestID string
	Now       time.Time
}

type ChangePasswordInput struct {
	Identity        Identity
	CurrentPassword []byte
	NewPassword     []byte
	RequestID       string
	Now             time.Time
}

func NewService(authStore Store, config ServiceConfig) *Service {
	maxConcurrentPasswordChecks := max(1, config.MaxConcurrentPasswordChecks)
	if config.MaxFailedLoginAttempts <= 0 {
		config.MaxFailedLoginAttempts = 5
	}
	if config.AccountLockDuration <= 0 {
		config.AccountLockDuration = 15 * time.Minute
	}
	return &Service{
		store:            authStore,
		config:           config,
		passwordParams:   DefaultPasswordParams(),
		passwordChecks:   make(chan struct{}, maxConcurrentPasswordChecks),
		passwordVerifier: VerifyPassword,
	}
}

func (service *Service) Login(ctx context.Context, input LoginInput) (LoginResult, error) {
	if strings.TrimSpace(input.RequestID) == "" || input.Now.IsZero() {
		return LoginResult{}, errors.New("login request ID and time are required")
	}
	if len(input.Password) > MaximumPasswordBytes {
		return LoginResult{}, service.rejectLogin(ctx, nil, input.RequestID, input.Now)
	}

	username, err := NormalizeUsername(input.Username)
	if err != nil {
		if err := service.verifyDummyPassword(ctx, input.Password); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, service.rejectLogin(ctx, nil, input.RequestID, input.Now)
	}

	user, err := service.store.FindUserByUsername(ctx, username)
	if errors.Is(err, store.ErrUserNotFound) {
		if err := service.verifyDummyPassword(ctx, input.Password); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, service.rejectLogin(ctx, nil, input.RequestID, input.Now)
	}
	if err != nil {
		return LoginResult{}, err
	}

	matches, needsRehash, err := service.verifyPassword(ctx, input.Password, user.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify stored password hash: %w", err)
	}
	lockActive := user.Status == "locked" &&
		user.LockExpiresAt != nil &&
		user.LockExpiresAt.After(input.Now)
	if !matches || user.Status == "disabled" || lockActive {
		return LoginResult{}, service.rejectLogin(ctx, &user.ID, input.RequestID, input.Now)
	}
	var replacementPasswordHash string
	if needsRehash {
		passwordHash, err := service.hashPassword(
			ctx,
			input.Password,
			service.passwordParams,
		)
		if err != nil {
			return LoginResult{}, err
		}
		replacementPasswordHash = passwordHash
	}

	sessionToken, sessionTokenDigest, err := NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrfToken, csrfTokenDigest, err := NewCSRFToken()
	if err != nil {
		return LoginResult{}, err
	}

	session, err := service.store.CompleteLogin(
		ctx,
		store.CompleteLoginParams{
			UserID:                    user.ID,
			ExpectedPasswordHash:      user.PasswordHash,
			ExpectedPasswordChangedAt: user.PasswordChangedAt,
			ReplacementPasswordHash:   replacementPasswordHash,
			Session: store.CreateSessionParams{
				UserID:          user.ID,
				TokenDigest:     sessionTokenDigest,
				CSRFTokenDigest: csrfTokenDigest,
				IdleExpiresAt:   input.Now.Add(service.config.SessionIdleTimeout),
				ExpiresAt:       input.Now.Add(service.config.SessionAbsoluteTimeout),
			},
			RequestID: input.RequestID,
			Now:       input.Now,
		},
	)
	if errors.Is(err, store.ErrCredentialsChanged) {
		return LoginResult{}, service.rejectLogin(ctx, &user.ID, input.RequestID, input.Now)
	}
	if err != nil {
		return LoginResult{}, err
	}
	if replacementPasswordHash != "" {
		user.PasswordHash = replacementPasswordHash
	}
	return LoginResult{
		User: User{
			ID:          user.ID,
			Username:    user.UsernameNormalized,
			DisplayName: user.DisplayName,
		},
		SessionID:    session.ID,
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		ExpiresAt:    session.ExpiresAt,
	}, nil
}

func (service *Service) Authenticate(
	ctx context.Context,
	sessionToken string,
	now time.Time,
) (Identity, error) {
	if sessionToken == "" || now.IsZero() {
		return Identity{}, ErrUnauthenticated
	}
	storedIdentity, err := service.store.FindActiveSession(
		ctx,
		DigestSessionToken(sessionToken),
		now,
		service.config.SessionIdleTimeout,
	)
	if errors.Is(err, store.ErrSessionNotFound) {
		return Identity{}, ErrUnauthenticated
	}
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		User: User{
			ID:          storedIdentity.User.ID,
			Username:    storedIdentity.User.UsernameNormalized,
			DisplayName: storedIdentity.User.DisplayName,
		},
		SessionID:       storedIdentity.Session.ID,
		ExpiresAt:       storedIdentity.Session.ExpiresAt,
		csrfTokenDigest: storedIdentity.Session.CSRFTokenDigest,
	}, nil
}

func (service *Service) Logout(
	ctx context.Context,
	identity Identity,
	requestID string,
	now time.Time,
) error {
	err := service.store.RevokeAuthenticatedSession(
		ctx,
		identity.SessionID,
		identity.User.ID,
		now,
		requestID,
	)
	if errors.Is(err, store.ErrSessionNotFound) {
		return ErrUnauthenticated
	}
	return err
}

func (service *Service) ChangePassword(
	ctx context.Context,
	input ChangePasswordInput,
) error {
	if strings.TrimSpace(input.Identity.User.ID) == "" ||
		strings.TrimSpace(input.Identity.SessionID) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() {
		return ErrInvalidCredentials
	}
	if len(input.CurrentPassword) == 0 ||
		len(input.CurrentPassword) > MaximumPasswordBytes {
		return service.rejectPasswordChange(ctx, input, "denied", ErrInvalidCredentials)
	}
	if err := ValidateNewPassword(input.NewPassword); err != nil {
		return service.rejectPasswordChange(ctx, input, "failed", ErrInvalidNewPassword)
	}
	user, err := service.store.FindUserByID(ctx, input.Identity.User.ID)
	if errors.Is(err, store.ErrUserNotFound) {
		return service.rejectPasswordChange(ctx, input, "denied", ErrUnauthenticated)
	}
	if err != nil {
		return err
	}
	if user.Status != "active" {
		return service.rejectPasswordChange(ctx, input, "denied", ErrUnauthenticated)
	}
	matches, _, err := service.verifyPassword(
		ctx,
		input.CurrentPassword,
		user.PasswordHash,
	)
	if err != nil {
		return service.rejectPasswordChange(
			ctx, input, "failed", fmt.Errorf("verify current password: %w", err),
		)
	}
	if !matches {
		return service.rejectPasswordChange(ctx, input, "denied", ErrInvalidCredentials)
	}
	samePassword, _, err := service.verifyPassword(
		ctx,
		input.NewPassword,
		user.PasswordHash,
	)
	if err != nil {
		return service.rejectPasswordChange(
			ctx, input, "failed", fmt.Errorf("compare new password: %w", err),
		)
	}
	if samePassword {
		return service.rejectPasswordChange(ctx, input, "failed", ErrPasswordUnchanged)
	}
	passwordHash, err := service.hashPassword(
		ctx,
		input.NewPassword,
		service.passwordParams,
	)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return service.rejectPasswordChange(
			ctx, input, "failed", fmt.Errorf("hash new password: %w", err),
		)
	}
	err = service.store.ChangeOwnPassword(ctx, store.ChangeOwnPasswordParams{
		UserID:                    user.ID,
		SessionID:                 input.Identity.SessionID,
		ExpectedPasswordHash:      user.PasswordHash,
		ExpectedPasswordChangedAt: user.PasswordChangedAt,
		NewPasswordHash:           passwordHash,
		RequestID:                 input.RequestID,
		Now:                       input.Now,
	})
	if errors.Is(err, store.ErrCredentialsChanged) {
		return service.rejectPasswordChange(ctx, input, "denied", ErrUnauthenticated)
	}
	return err
}

func (service *Service) rejectPasswordChange(
	ctx context.Context,
	input ChangePasswordInput,
	result string,
	rejection error,
) error {
	ctx, cancel := auditctx.Detach(ctx, securityWriteTimeout)
	defer cancel()
	if err := service.store.RecordPasswordChangeAudit(
		ctx,
		input.Identity.User.ID,
		result,
		input.RequestID,
		input.Now,
	); err != nil {
		return err
	}
	return rejection
}

func (service *Service) CSRFTokenMatches(identity Identity, token string) bool {
	return CSRFTokenMatches(token, identity.csrfTokenDigest)
}

func (service *Service) RecordLoginDenied(ctx context.Context, requestID string) error {
	ctx, cancel := auditctx.Detach(ctx, securityWriteTimeout)
	defer cancel()
	return service.store.RecordLoginAudit(ctx, nil, "denied", requestID)
}

func CSRFTokenMatches(token string, expectedDigest []byte) bool {
	if token == "" || len(expectedDigest) == 0 {
		return false
	}
	actualDigest := DigestCSRFToken(token)
	return subtle.ConstantTimeCompare(actualDigest, expectedDigest) == 1
}

func (service *Service) rejectLogin(
	ctx context.Context,
	targetUserID *string,
	requestID string,
	now time.Time,
) error {
	// Detached: this write is both the audit record and the persistent lockout
	// counter. On the request context a client that hangs up after sending a
	// wrong password would leave neither behind, letting the attempt cost
	// nothing and keeping the account from ever locking.
	ctx, cancel := auditctx.Detach(ctx, securityWriteTimeout)
	defer cancel()
	if err := service.store.RecordLoginFailure(ctx, store.RecordLoginFailureParams{
		UserID:       targetUserID,
		RequestID:    requestID,
		Now:          now,
		MaxFailures:  service.config.MaxFailedLoginAttempts,
		LockDuration: service.config.AccountLockDuration,
	}); err != nil {
		return err
	}
	return ErrInvalidCredentials
}

func (service *Service) verifyDummyPassword(ctx context.Context, password []byte) error {
	_, _, err := service.verifyPassword(ctx, password, dummyPasswordHash)
	if err != nil {
		return fmt.Errorf("verify dummy password hash: %w", err)
	}
	return nil
}

func (service *Service) verifyPassword(
	ctx context.Context,
	password []byte,
	encoded string,
) (bool, bool, error) {
	release, err := service.acquirePasswordCheck(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()

	matches, needsRehash, err := service.passwordVerifier(password, encoded)
	if err != nil {
		return false, false, err
	}
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	return matches, needsRehash, nil
}

func (service *Service) hashPassword(
	ctx context.Context,
	password []byte,
	params PasswordParams,
) (string, error) {
	release, err := service.acquirePasswordCheck(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	passwordHash, err := HashPassword(password, params)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return passwordHash, nil
}

func (service *Service) acquirePasswordCheck(
	ctx context.Context,
) (func(), error) {
	select {
	case service.passwordChecks <- struct{}{}:
		return func() {
			<-service.passwordChecks
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

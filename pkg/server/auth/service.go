package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"

var ErrInvalidCredentials = errors.New("invalid username or password")

var ErrUnauthenticated = errors.New("authentication required")

type ServiceConfig struct {
	SessionIdleTimeout          time.Duration
	SessionAbsoluteTimeout      time.Duration
	MaxConcurrentPasswordChecks int
}

type Service struct {
	store            *store.AuthStore
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

func NewService(authStore *store.AuthStore, config ServiceConfig) *Service {
	maxConcurrentPasswordChecks := max(1, config.MaxConcurrentPasswordChecks)
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
		return LoginResult{}, service.rejectLogin(ctx, nil, input.RequestID)
	}

	username, err := NormalizeUsername(input.Username)
	if err != nil {
		if err := service.verifyDummyPassword(ctx, input.Password); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, service.rejectLogin(ctx, nil, input.RequestID)
	}

	user, err := service.store.FindUserByUsername(ctx, username)
	if errors.Is(err, store.ErrUserNotFound) {
		if err := service.verifyDummyPassword(ctx, input.Password); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, service.rejectLogin(ctx, nil, input.RequestID)
	}
	if err != nil {
		return LoginResult{}, err
	}

	matches, needsRehash, err := service.verifyPassword(ctx, input.Password, user.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify stored password hash: %w", err)
	}
	if !matches || user.Status != "active" {
		return LoginResult{}, service.rejectLogin(ctx, &user.ID, input.RequestID)
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
		},
	)
	if errors.Is(err, store.ErrCredentialsChanged) {
		return LoginResult{}, service.rejectLogin(ctx, &user.ID, input.RequestID)
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

func (service *Service) CSRFTokenMatches(identity Identity, token string) bool {
	return CSRFTokenMatches(token, identity.csrfTokenDigest)
}

func (service *Service) RecordLoginDenied(ctx context.Context, requestID string) error {
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
) error {
	if err := service.store.RecordLoginAudit(ctx, targetUserID, "failed", requestID); err != nil {
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

package enrollment

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const maxClusterNameBytes = 253

type Service struct {
	store             *store.EnrollmentStore
	tokenTTL          time.Duration
	certificateSigner *CertificateSigner
}

type ServiceConfig struct {
	TokenTTL          time.Duration
	CertificateSigner *CertificateSigner
}

func NewService(enrollmentStore *store.EnrollmentStore, config ServiceConfig) *Service {
	return &Service{
		store:             enrollmentStore,
		tokenTTL:          config.TokenTTL,
		certificateSigner: config.CertificateSigner,
	}
}

func (service *Service) Create(
	ctx context.Context,
	input CreateInput,
) (CreateResult, error) {
	if !validation.IsUUID(input.ProjectID) ||
		!validation.IsUUID(input.UserID) ||
		!validBoundedValue(input.ClusterName, maxClusterNameBytes) ||
		strings.TrimSpace(input.RequestID) == "" ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) ||
		input.Now.IsZero() {
		return CreateResult{}, ErrInvalidInput
	}
	if service.tokenTTL <= 0 {
		return CreateResult{}, errors.New("enrollment token TTL must be greater than zero")
	}

	token, tokenDigest, err := newToken()
	if err != nil {
		return CreateResult{}, err
	}
	storedEnrollment, err := service.store.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       input.ProjectID,
			ClusterName:     input.ClusterName,
			CreatedByUserID: input.UserID,
			TokenDigest:     tokenDigest,
			ExpiresAt:       input.Now.Add(service.tokenTTL),
			RequestID:       input.RequestID,
			IdempotencyKey:  input.IdempotencyKey,
		},
	)
	if errors.Is(err, store.ErrEnrollmentCreationDenied) {
		return CreateResult{}, ErrDenied
	}
	if errors.Is(err, store.ErrEnrollmentIdempotencyConflict) {
		return CreateResult{}, ErrIdempotencyConflict
	}
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{
		ID:          storedEnrollment.ID,
		ClusterName: storedEnrollment.ClusterName,
		Token:       token,
		ExpiresAt:   storedEnrollment.ExpiresAt,
	}, nil
}

func (service *Service) ResolveManifest(
	ctx context.Context,
	token string,
	now time.Time,
) (ManifestEnrollment, error) {
	if now.IsZero() {
		return ManifestEnrollment{}, ErrInvalidInput
	}
	tokenDigest, err := digestToken(token)
	if err != nil {
		return ManifestEnrollment{}, ErrTokenRejected
	}
	stored, err := service.store.FindActiveEnrollmentByTokenDigest(
		ctx,
		tokenDigest,
		now,
	)
	if errors.Is(err, store.ErrEnrollmentTokenRejected) {
		return ManifestEnrollment{}, ErrTokenRejected
	}
	if err != nil {
		return ManifestEnrollment{}, err
	}
	return ManifestEnrollment{
		ID:          stored.ID,
		TenantID:    stored.TenantID,
		ProjectID:   stored.ProjectID,
		ClusterName: stored.ClusterName,
		ExpiresAt:   stored.ExpiresAt,
	}, nil
}

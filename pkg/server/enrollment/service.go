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
	newCluster := input.ClusterID == ""
	if !validation.IsUUID(input.ProjectID) ||
		(!newCluster && !validation.IsUUID(input.ClusterID)) ||
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
			ClusterID:       input.ClusterID,
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
		ClusterID:   storedEnrollment.ClusterID,
		ClusterName: storedEnrollment.ClusterName,
		Token:       token,
		ExpiresAt:   storedEnrollment.ExpiresAt,
	}, nil
}

func (service *Service) CreateReenrollment(
	ctx context.Context,
	input ReenrollInput,
) (CreateResult, error) {
	if !validation.IsUUID(input.ClusterID) || !validation.IsUUID(input.UserID) ||
		strings.TrimSpace(input.RequestID) == "" ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) || input.Now.IsZero() {
		return CreateResult{}, ErrInvalidInput
	}
	target, err := service.store.GetClusterEnrollmentTarget(ctx, input.ClusterID)
	if errors.Is(err, store.ErrClusterNotFound) {
		return CreateResult{}, ErrNotFound
	}
	if err != nil {
		return CreateResult{}, err
	}
	result, err := service.Create(ctx, CreateInput{
		ProjectID: target.ProjectID, ClusterID: input.ClusterID,
		ClusterName: target.ClusterName, UserID: input.UserID,
		RequestID: input.RequestID, IdempotencyKey: input.IdempotencyKey,
		Now: input.Now,
	})
	if errors.Is(err, ErrDenied) {
		return CreateResult{}, ErrStateConflict
	}
	return result, err
}

func (service *Service) List(
	ctx context.Context,
	projectID string,
	now time.Time,
) ([]Enrollment, error) {
	if !validation.IsUUID(projectID) || now.IsZero() {
		return nil, ErrInvalidInput
	}
	items, err := service.store.ListEnrollments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]Enrollment, 0, len(items))
	for _, item := range items {
		result = append(result, enrollmentFromStore(item, now))
	}
	return result, nil
}

func (service *Service) Get(
	ctx context.Context,
	projectID string,
	enrollmentID string,
	now time.Time,
) (Enrollment, error) {
	if !validation.IsUUID(projectID) || !validation.IsUUID(enrollmentID) ||
		now.IsZero() {
		return Enrollment{}, ErrInvalidInput
	}
	item, err := service.store.GetEnrollment(ctx, projectID, enrollmentID)
	if errors.Is(err, store.ErrEnrollmentNotFound) {
		return Enrollment{}, ErrNotFound
	}
	if err != nil {
		return Enrollment{}, err
	}
	return enrollmentFromStore(item, now), nil
}

func (service *Service) Revoke(
	ctx context.Context,
	input RevokeInput,
) (Enrollment, error) {
	if !input.Confirm || !validation.IsUUID(input.ProjectID) ||
		!validation.IsUUID(input.EnrollmentID) ||
		!validation.IsUUID(input.UserID) ||
		strings.TrimSpace(input.RequestID) == "" || input.Now.IsZero() {
		return Enrollment{}, ErrInvalidInput
	}
	item, err := service.store.RevokeEnrollment(ctx, store.RevokeEnrollmentParams{
		ProjectID: input.ProjectID, EnrollmentID: input.EnrollmentID,
		ActorUserID: input.UserID, RequestID: input.RequestID, Now: input.Now,
	})
	switch {
	case errors.Is(err, store.ErrEnrollmentNotFound):
		return Enrollment{}, ErrNotFound
	case errors.Is(err, store.ErrEnrollmentStateConflict):
		return Enrollment{}, ErrStateConflict
	case err != nil:
		return Enrollment{}, err
	default:
		return enrollmentFromStore(item, input.Now), nil
	}
}

func enrollmentFromStore(item store.Enrollment, now time.Time) Enrollment {
	status := "active"
	switch {
	case item.RevokedAt != nil:
		status = "revoked"
	case item.ConsumedAt != nil:
		status = "consumed"
	case !item.ExpiresAt.After(now):
		status = "expired"
	}
	return Enrollment{
		ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID,
		ClusterID: item.ClusterID, ClusterName: item.ClusterName,
		CreatedByUserID: item.CreatedByUserID,
		Status:          status, ExpiresAt: item.ExpiresAt, ConsumedAt: item.ConsumedAt,
		RevokedAt: item.RevokedAt, CreatedAt: item.CreatedAt,
	}
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

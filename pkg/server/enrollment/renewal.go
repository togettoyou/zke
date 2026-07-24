package enrollment

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

type CertificateRenewalService struct {
	store  *store.AgentConnectionStore
	signer *CertificateSigner
}

type RenewCertificateInput struct {
	Identity                 store.AgentConnectionIdentity
	CurrentCertificateSerial string
	CSRPEM                   []byte
	RequestID                string
	Now                      time.Time
}

type RenewCertificateResult struct {
	CertificatePEM       string
	CertificateExpiresAt time.Time
}

func NewCertificateRenewalService(
	connectionStore *store.AgentConnectionStore,
	signer *CertificateSigner,
) *CertificateRenewalService {
	return &CertificateRenewalService{
		store:  connectionStore,
		signer: signer,
	}
}

func (service *CertificateRenewalService) Renew(
	ctx context.Context,
	input RenewCertificateInput,
) (RenewCertificateResult, error) {
	if service == nil || service.store == nil || service.signer == nil {
		return RenewCertificateResult{}, ErrSigningUnavailable
	}
	if !validConnectionIdentity(input.Identity) ||
		strings.TrimSpace(input.CurrentCertificateSerial) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() {
		return RenewCertificateResult{}, ErrInvalidInput
	}
	certificateRequest, fingerprint, err := parseCertificateRequest(input.CSRPEM)
	if err != nil {
		return RenewCertificateResult{}, ErrInvalidInput
	}
	signed, err := service.signer.Sign(
		certificateRequest,
		CertificateIdentity{
			TenantID:  input.Identity.TenantID,
			ProjectID: input.Identity.ProjectID,
			ClusterID: input.Identity.ClusterID,
			AgentID:   input.Identity.AgentID,
		},
		input.Now,
	)
	if err != nil {
		return RenewCertificateResult{}, err
	}
	stored, err := service.store.RenewCredential(
		ctx,
		store.RenewAgentCredentialParams{
			Identity:                 input.Identity,
			CurrentCertificateSerial: input.CurrentCertificateSerial,
			CSRFingerprint:           fingerprint,
			NewCertificateSerial:     signed.Serial,
			CertificatePEM:           signed.PEM,
			CertificateExpiresAt:     signed.ExpiresAt,
			RequestID:                input.RequestID,
			Now:                      input.Now,
		},
	)
	if err != nil {
		if errors.Is(err, store.ErrAgentCredentialRejected) {
			return RenewCertificateResult{}, ErrCredentialRejected
		}
		return RenewCertificateResult{}, err
	}
	return RenewCertificateResult{
		CertificatePEM:       stored.PEM,
		CertificateExpiresAt: stored.ExpiresAt,
	}, nil
}

func validConnectionIdentity(identity store.AgentConnectionIdentity) bool {
	return validation.IsUUID(identity.TenantID) &&
		validation.IsUUID(identity.ProjectID) &&
		validation.IsUUID(identity.ClusterID) &&
		validation.IsUUID(identity.AgentID)
}

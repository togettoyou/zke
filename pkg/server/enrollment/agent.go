package enrollment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	maxCSRPEMBytes         = 64 << 10
	maxCertificatePEMBytes = 1 << 20
	maxClusterNameBytes    = 253
	maxVersionBytes        = 128
)

func (service *Service) Begin(
	ctx context.Context,
	input BeginInput,
) (BeginResult, error) {
	if !validation.IsIdempotencyKey(input.IdempotencyKey) ||
		strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() {
		return BeginResult{}, ErrInvalidInput
	}
	tokenDigest, err := digestToken(input.Token)
	if err != nil {
		return BeginResult{}, err
	}
	_, csrFingerprint, err := parseCertificateRequest(input.CSRPEM)
	if err != nil {
		return BeginResult{}, ErrInvalidInput
	}

	attempt, err := service.store.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    tokenDigest,
			IdempotencyKey: input.IdempotencyKey,
			CSRFingerprint: csrFingerprint,
			RequestID:      input.RequestID,
			Now:            input.Now,
		},
	)
	if err != nil {
		return BeginResult{}, mapStoreError(err)
	}
	result := BeginResult{
		AttemptID:      attempt.ID,
		EnrollmentID:   attempt.EnrollmentID,
		TenantID:       attempt.TenantID,
		ProjectID:      attempt.ProjectID,
		IdempotencyKey: attempt.IdempotencyKey,
		CSRFingerprint: append([]byte(nil), attempt.CSRFingerprint...),
		Status:         AttemptStatus(attempt.Status),
	}
	if attempt.Result != nil {
		result.Result = &AgentEnrollmentResult{
			ClusterID:            attempt.Result.ClusterID,
			AgentID:              attempt.Result.AgentID,
			CertificatePEM:       attempt.Result.CertificatePEM,
			CertificateExpiresAt: attempt.Result.CertificateExpiresAt,
		}
	}
	return result, nil
}

func (service *Service) Complete(
	ctx context.Context,
	input CompleteInput,
) (AgentEnrollmentResult, error) {
	if !validation.IsUUID(input.EnrollmentID) ||
		!validation.IsUUID(input.AttemptID) ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) ||
		!validation.IsUUID(input.ClusterID) ||
		!validation.IsUUID(input.AgentID) ||
		!validBoundedValue(input.ClusterName, maxClusterNameBytes) ||
		!validBoundedValue(input.AgentVersion, maxVersionBytes) ||
		!validBoundedValue(input.ProtocolVersion, maxVersionBytes) ||
		strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() {
		return AgentEnrollmentResult{}, ErrInvalidInput
	}
	certificateRequest, csrFingerprint, err := parseCertificateRequest(input.CSRPEM)
	if err != nil {
		return AgentEnrollmentResult{}, ErrInvalidInput
	}
	certificate, err := parseLeafCertificate(input.CertificatePEM)
	if err != nil ||
		!certificate.NotAfter.After(input.Now) ||
		!publicKeysEqual(certificateRequest, certificate) {
		return AgentEnrollmentResult{}, ErrInvalidInput
	}

	result, err := service.store.CompleteAgentEnrollment(
		ctx,
		store.CompleteAgentEnrollmentParams{
			EnrollmentID:         input.EnrollmentID,
			AttemptID:            input.AttemptID,
			IdempotencyKey:       input.IdempotencyKey,
			CSRFingerprint:       csrFingerprint,
			ClusterID:            input.ClusterID,
			AgentID:              input.AgentID,
			ClusterName:          input.ClusterName,
			AgentVersion:         input.AgentVersion,
			ProtocolVersion:      input.ProtocolVersion,
			CertificateSerial:    certificate.SerialNumber.String(),
			CertificatePEM:       input.CertificatePEM,
			CertificateExpiresAt: certificate.NotAfter,
			RequestID:            input.RequestID,
			Now:                  input.Now,
		},
	)
	if err != nil {
		return AgentEnrollmentResult{}, mapStoreError(err)
	}
	return AgentEnrollmentResult{
		ClusterID:            result.ClusterID,
		AgentID:              result.AgentID,
		CertificatePEM:       result.CertificatePEM,
		CertificateExpiresAt: result.CertificateExpiresAt,
	}, nil
}

func (service *Service) Enroll(
	ctx context.Context,
	input EnrollInput,
) (EnrollResult, error) {
	if !validBoundedValue(input.ClusterName, maxClusterNameBytes) ||
		!validBoundedValue(input.AgentVersion, maxVersionBytes) ||
		!validBoundedValue(input.ProtocolVersion, maxVersionBytes) {
		return EnrollResult{}, ErrInvalidInput
	}
	attempt, err := service.Begin(ctx, BeginInput{
		Token:          input.Token,
		IdempotencyKey: input.IdempotencyKey,
		CSRPEM:         input.CSRPEM,
		RequestID:      input.RequestID,
		Now:            input.Now,
	})
	if err != nil {
		return EnrollResult{}, err
	}
	if attempt.Result != nil {
		return EnrollResult{
			AgentEnrollmentResult: *attempt.Result,
			Replayed:              true,
		}, nil
	}
	if service.certificateSigner == nil {
		return EnrollResult{}, service.recordEnrollmentFailure(
			ctx,
			attempt.EnrollmentID,
			input.RequestID,
			ErrSigningUnavailable,
		)
	}

	clusterID, err := newUUID()
	if err != nil {
		return EnrollResult{}, service.recordEnrollmentFailure(
			ctx,
			attempt.EnrollmentID,
			input.RequestID,
			err,
		)
	}
	agentID, err := newUUID()
	if err != nil {
		return EnrollResult{}, service.recordEnrollmentFailure(
			ctx,
			attempt.EnrollmentID,
			input.RequestID,
			err,
		)
	}
	certificateRequest, _, err := parseCertificateRequest(input.CSRPEM)
	if err != nil {
		return EnrollResult{}, ErrInvalidInput
	}
	signedCertificate, err := service.certificateSigner.Sign(
		certificateRequest,
		CertificateIdentity{
			TenantID:  attempt.TenantID,
			ProjectID: attempt.ProjectID,
			ClusterID: clusterID,
			AgentID:   agentID,
		},
		input.Now,
	)
	if err != nil {
		return EnrollResult{}, service.recordEnrollmentFailure(
			ctx,
			attempt.EnrollmentID,
			input.RequestID,
			err,
		)
	}
	result, err := service.Complete(ctx, CompleteInput{
		EnrollmentID:    attempt.EnrollmentID,
		AttemptID:       attempt.AttemptID,
		IdempotencyKey:  attempt.IdempotencyKey,
		CSRPEM:          input.CSRPEM,
		ClusterID:       clusterID,
		AgentID:         agentID,
		ClusterName:     input.ClusterName,
		AgentVersion:    input.AgentVersion,
		ProtocolVersion: input.ProtocolVersion,
		CertificatePEM:  signedCertificate.PEM,
		RequestID:       input.RequestID,
		Now:             input.Now,
	})
	if err != nil {
		if !errors.Is(err, ErrTokenRejected) &&
			!errors.Is(err, ErrAttemptConflict) &&
			!errors.Is(err, ErrAttemptFailed) {
			err = service.recordEnrollmentFailure(
				ctx,
				attempt.EnrollmentID,
				input.RequestID,
				err,
			)
		}
		return EnrollResult{}, err
	}
	return EnrollResult{AgentEnrollmentResult: result}, nil
}

func (service *Service) recordEnrollmentFailure(
	ctx context.Context,
	enrollmentID string,
	requestID string,
	cause error,
) error {
	if service.store == nil {
		return cause
	}
	if err := service.store.RecordAgentEnrollmentFailure(
		ctx,
		enrollmentID,
		requestID,
	); err != nil {
		return errors.Join(
			cause,
			fmt.Errorf("record Agent enrollment failure audit: %w", err),
		)
	}
	return cause
}

func parseCertificateRequest(csrPEM []byte) (*x509.CertificateRequest, []byte, error) {
	if len(csrPEM) == 0 || len(csrPEM) > maxCSRPEMBytes {
		return nil, nil, errors.New("CSR PEM size is invalid")
	}
	block, rest := pem.Decode(csrPEM)
	if block == nil ||
		(block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST") ||
		len(block.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("CSR PEM is invalid")
	}
	certificateRequest, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, nil, errors.New("parse CSR")
	}
	if err := certificateRequest.CheckSignature(); err != nil {
		return nil, nil, errors.New("verify CSR signature")
	}
	if err := validateCertificatePublicKey(certificateRequest.PublicKey, "Agent"); err != nil {
		return nil, nil, err
	}
	fingerprint := sha256.Sum256(certificateRequest.Raw)
	return certificateRequest, fingerprint[:], nil
}

func parseLeafCertificate(certificatePEM string) (*x509.Certificate, error) {
	if len(certificatePEM) == 0 || len(certificatePEM) > maxCertificatePEMBytes {
		return nil, errors.New("certificate PEM size is invalid")
	}
	remaining := []byte(certificatePEM)
	block, remaining := pem.Decode(remaining)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
		return nil, errors.New("certificate PEM is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("parse certificate")
	}
	for len(bytes.TrimSpace(remaining)) != 0 {
		var chainBlock *pem.Block
		chainBlock, remaining = pem.Decode(remaining)
		if chainBlock == nil ||
			chainBlock.Type != "CERTIFICATE" ||
			len(chainBlock.Headers) != 0 {
			return nil, errors.New("certificate chain PEM is invalid")
		}
		if _, err := x509.ParseCertificate(chainBlock.Bytes); err != nil {
			return nil, errors.New("parse certificate chain")
		}
	}
	return certificate, nil
}

func publicKeysEqual(
	certificateRequest *x509.CertificateRequest,
	certificate *x509.Certificate,
) bool {
	csrPublicKey, err := x509.MarshalPKIXPublicKey(certificateRequest.PublicKey)
	if err != nil {
		return false
	}
	certificatePublicKey, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return false
	}
	return bytes.Equal(csrPublicKey, certificatePublicKey)
}

func validBoundedValue(value string, maximumBytes int) bool {
	return len(value) > 0 &&
		len(value) <= maximumBytes &&
		strings.TrimSpace(value) == value
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrEnrollmentTokenRejected):
		return ErrTokenRejected
	case errors.Is(err, store.ErrEnrollmentAttemptConflict),
		errors.Is(err, store.ErrEnrollmentAttemptNotFound):
		return ErrAttemptConflict
	case errors.Is(err, store.ErrEnrollmentAttemptFailed):
		return ErrAttemptFailed
	default:
		return err
	}
}

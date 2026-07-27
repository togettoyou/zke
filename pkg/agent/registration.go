package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	agentEnrollmentPath        = "/agent-api/v1/enroll"
	agentProtocolVersion       = "v1"
	maxEnrollmentTokenBytes    = 4 << 10
	maxEnrollmentResponseBytes = 2 << 20
	maxCACertificateFileBytes  = 1 << 20
)

type registrationClient struct {
	endpoint string
	client   *http.Client
}

type registrationRequest struct {
	CSRPEM          string `json:"csr_pem"`
	AgentVersion    string `json:"agent_version"`
	ProtocolVersion string `json:"protocol_version"`
}

type registrationResponse struct {
	ClusterID            string    `json:"cluster_id"`
	AgentID              string    `json:"agent_id"`
	CertificatePEM       string    `json:"certificate_pem"`
	CertificateExpiresAt time.Time `json:"certificate_expires_at"`
}

type registrationResponseEnvelope struct {
	Code    int                  `json:"code"`
	Message string               `json:"message"`
	Data    registrationResponse `json:"data"`
}

type registrationAPIErrorData struct {
	ErrorCode string `json:"error_code"`
}

type registrationAPIError struct {
	Code int                      `json:"code"`
	Data registrationAPIErrorData `json:"data"`
}

type registrationError struct {
	statusCode int
	code       string
	retryAfter time.Duration
	retryable  bool
}

type registrationResponseError struct {
	retryable bool
	err       error
}

func (err *registrationResponseError) Error() string {
	return "decode Agent enrollment response"
}

func (err *registrationResponseError) Unwrap() error {
	return err.err
}

func (err *registrationError) Error() string {
	if err.code == "" {
		return fmt.Sprintf("Agent enrollment request failed with HTTP status %d", err.statusCode)
	}
	return fmt.Sprintf(
		"Agent enrollment request failed with HTTP status %d (%s)",
		err.statusCode,
		err.code,
	)
}

func newRegistrationClient(cfg Config) (*registrationClient, error) {
	serverURL, err := url.Parse(cfg.Registration.ServerURL)
	if err != nil {
		return nil, errors.New("parse Agent Server address")
	}
	serverURL.Path = agentEnrollmentPath

	rootCAs, err := x509.SystemCertPool()
	if err != nil || rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	if cfg.Registration.CACertificateFile != "" {
		certificatePEM, err := readBoundedFile(
			cfg.Registration.CACertificateFile,
			maxCACertificateFileBytes,
			"registration CA certificate file",
		)
		if err != nil {
			return nil, err
		}
		if err := appendRootCertificates(rootCAs, certificatePEM); err != nil {
			return nil, errors.New(
				"registration CA certificate file does not contain a valid certificate",
			)
		}
	}
	if len(cfg.Registration.CACertificatePEM) != 0 {
		if err := appendRootCertificates(
			rootCAs,
			cfg.Registration.CACertificatePEM,
		); err != nil {
			return nil, errors.New(
				"registration CA certificate Secret does not contain a valid certificate",
			)
		}
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.Registration.Timeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    rootCAs,
		},
	}
	return &registrationClient{
		endpoint: serverURL.String(),
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Registration.Timeout,
			CheckRedirect: func(
				_ *http.Request,
				_ []*http.Request,
			) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (client *registrationClient) Enroll(
	ctx context.Context,
	token string,
	pending PendingIdentity,
	agentVersion string,
) (RegistrationIdentity, error) {
	requestBody, err := json.Marshal(registrationRequest{
		CSRPEM:          string(pending.CSRPEM),
		AgentVersion:    agentVersion,
		ProtocolVersion: agentProtocolVersion,
	})
	if err != nil {
		return RegistrationIdentity{}, errors.New("encode Agent enrollment request")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.endpoint,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return RegistrationIdentity{}, errors.New("create Agent enrollment request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", pending.IdempotencyKey)

	response, err := client.client.Do(request)
	if err != nil {
		return RegistrationIdentity{}, fmt.Errorf("send Agent enrollment request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated &&
		response.StatusCode != http.StatusOK {
		return RegistrationIdentity{}, decodeRegistrationError(response)
	}

	var envelope registrationResponseEnvelope
	if err := decodeBoundedJSON(
		response.Body,
		maxEnrollmentResponseBytes,
		&envelope,
	); err != nil {
		return RegistrationIdentity{}, &registrationResponseError{
			retryable: retryableResponseReadError(err),
			err:       err,
		}
	}
	if envelope.Code != response.StatusCode ||
		envelope.Message != "Success" {
		return RegistrationIdentity{}, &registrationResponseError{
			err: errors.New("invalid response envelope"),
		}
	}
	result := envelope.Data
	return RegistrationIdentity{
		ClusterID:            result.ClusterID,
		AgentID:              result.AgentID,
		CertificatePEM:       []byte(result.CertificatePEM),
		CertificateExpiresAt: result.CertificateExpiresAt,
	}, nil
}

func retryableResponseReadError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func validateEnrollmentToken(value []byte) (string, error) {
	if len(value) == 0 || len(value) > maxEnrollmentTokenBytes {
		return "", errors.New(
			"Agent enrollment Secret contains an invalid token",
		)
	}
	token := strings.TrimSpace(string(value))
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", errors.New(
			"Agent enrollment Secret contains an invalid token",
		)
	}
	return token, nil
}

func readBoundedFile(path string, maximum int64, description string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	defer file.Close()

	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", description, err)
	}
	if len(value) == 0 || int64(len(value)) > maximum {
		return nil, fmt.Errorf("%s size is invalid", description)
	}
	return value, nil
}

func decodeRegistrationError(response *http.Response) error {
	var apiError registrationAPIError
	_ = decodeBoundedJSON(response.Body, 64<<10, &apiError)
	if apiError.Code != response.StatusCode ||
		!validRegistrationErrorCode(apiError.Data.ErrorCode) {
		apiError.Data.ErrorCode = ""
	}
	retryable := response.StatusCode == http.StatusRequestTimeout ||
		response.StatusCode == http.StatusTooEarly ||
		response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode >= http.StatusInternalServerError
	return &registrationError{
		statusCode: response.StatusCode,
		code:       apiError.Data.ErrorCode,
		retryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now().UTC()),
		retryable:  retryable,
	}
}

func appendRootCertificates(pool *x509.CertPool, value []byte) error {
	remaining := value
	added := false
	for len(bytes.TrimSpace(remaining)) != 0 {
		block, rest := pem.Decode(remaining)
		if block == nil ||
			block.Type != "CERTIFICATE" ||
			len(block.Headers) != 0 {
			return errors.New("invalid certificate PEM")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return errors.New("parse certificate")
		}
		pool.AddCert(certificate)
		added = true
		remaining = rest
	}
	if !added {
		return errors.New("certificate PEM is empty")
	}
	return nil
}

func validRegistrationErrorCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' {
			continue
		}
		return false
	}
	return true
}

func decodeBoundedJSON(reader io.Reader, maximum int64, target any) error {
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	if limited.N <= 0 {
		return errors.New("JSON body exceeds the size limit")
	}
	return nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	retryAt, err := http.ParseTime(value)
	if err != nil || !retryAt.After(now) {
		return 0
	}
	return retryAt.Sub(now)
}

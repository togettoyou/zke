package helm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

// How this Server talks to a chart repository.
//
// One place for the transport because two protocols share it: the index and the
// plain archives are ordinary HTTP GETs, and an OCI pull rides the same client
// so a repository's private CA and its "skip verification" choice mean the same
// thing whichever way its charts are published.

const (
	// Bounds on one upstream request. They apply to the whole exchange, so a
	// repository that accepts the connection and then never sends a body
	// cannot hold a Server request open.
	upstreamRequestTimeout = 60 * time.Second
	upstreamDialTimeout    = 10 * time.Second
)

var errUpstreamTooLarge = errors.New("upstream response exceeds the allowed size")

// unreachableError is a repository failure whose account is worth returning to
// the administrator who configured it. The URL is theirs, the status code is
// the repository's answer, and "could not be read" on its own would send them
// looking at ZKE rather than at the repository.
//
// It implements the HTTP layer's detailed-error interface, so the detail
// replaces the mapping's fixed message. Nothing built here ever carries a
// credential: the credential is sent as a header, and redactError removes one
// that a redirect put into a URL.
type unreachableError struct {
	detail string
}

func (err *unreachableError) Error() string  { return err.detail }
func (err *unreachableError) Detail() string { return err.detail }
func (err *unreachableError) Unwrap() error  { return ErrRepositoryUnreachable }

func unreachable(format string, arguments ...any) error {
	return &unreachableError{detail: fmt.Sprintf(format, arguments...)}
}

// upstreamRequest is one GET against a repository.
//
// The validators are optional and only ever come from something this Server
// already has on disk: sending them turns re-reading an expired index into a
// conditional request, which a repository that has not changed answers with a
// 304 and no body at all.
type upstreamRequest struct {
	Target       string
	MaxBytes     int64
	ETag         string
	LastModified string
}

// upstreamResponse carries the body and the validators to store with it.
type upstreamResponse struct {
	// Body is empty when NotModified is set: the caller already has it.
	Body         []byte
	NotModified  bool
	ETag         string
	LastModified string
}

// get performs one bounded, unconditional GET and returns the body.
func (service *Service) get(
	ctx context.Context,
	repository store.HelmRepository,
	target string,
	maxBytes int64,
) ([]byte, error) {
	response, err := service.fetch(ctx, repository, upstreamRequest{
		Target:   target,
		MaxBytes: maxBytes,
	})
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

// fetch performs one bounded HTTP GET against a repository.
//
// Everything about the request is decided here rather than by a shared client:
// the trust store, whether verification is skipped, and the credential are all
// properties of the one repository being read, and a client shared between
// repositories would carry one repository's settings into another's request.
func (service *Service) fetch(
	ctx context.Context,
	repository store.HelmRepository,
	request upstreamRequest,
) (upstreamResponse, error) {
	parsed, err := url.Parse(request.Target)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return upstreamResponse{}, unreachable(
			"%s is not an http or https URL",
			request.Target,
		)
	}
	client, err := service.clientFor(repository)
	if err != nil {
		return upstreamResponse{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, upstreamRequestTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		request.Target,
		nil,
	)
	if err != nil {
		return upstreamResponse{}, unreachable("%s", err)
	}
	httpRequest.Header.Set("User-Agent", service.userAgent)
	httpRequest.Header.Set("Accept", "application/octet-stream, text/yaml, */*")
	if request.ETag != "" {
		httpRequest.Header.Set("If-None-Match", request.ETag)
	}
	if request.LastModified != "" {
		httpRequest.Header.Set("If-Modified-Since", request.LastModified)
	}
	if repository.Username != "" || repository.Password != "" {
		httpRequest.SetBasicAuth(repository.Username, repository.Password)
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		// The error carries the URL, which is fine — an administrator entered
		// it — but never the credential, which is why it is set as a header
		// rather than embedded in the URL.
		return upstreamResponse{}, unreachable("%s", redactError(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNotModified {
		return upstreamResponse{
			NotModified:  true,
			ETag:         response.Header.Get("ETag"),
			LastModified: response.Header.Get("Last-Modified"),
		}, nil
	}
	if response.StatusCode == http.StatusNotFound {
		return upstreamResponse{}, ErrChartNotFound
	}
	if response.StatusCode != http.StatusOK {
		return upstreamResponse{}, unreachable(
			"repository answered %s",
			response.Status,
		)
	}
	// One byte past the ceiling on purpose: it is the difference between a body
	// that just fits and one that does not.
	body, err := io.ReadAll(io.LimitReader(response.Body, request.MaxBytes+1))
	if err != nil {
		return upstreamResponse{}, unreachable("%s", redactError(err))
	}
	if int64(len(body)) > request.MaxBytes {
		return upstreamResponse{}, errUpstreamTooLarge
	}
	return upstreamResponse{
		Body:         body,
		ETag:         response.Header.Get("ETag"),
		LastModified: response.Header.Get("Last-Modified"),
	}, nil
}

func (service *Service) clientFor(repository store.HelmRepository) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   upstreamDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   upstreamDialTimeout,
		ResponseHeaderTimeout: upstreamRequestTimeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   2,
	}
	if repository.CACertificatePEM != "" || repository.InsecureSkipTLSVerify {
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			//nolint:gosec // Skipping verification is an explicit, stored
			// choice by a global administrator, reported by every read of the
			// catalogue rather than assumed.
			InsecureSkipVerify: repository.InsecureSkipTLSVerify,
		}
		if repository.CACertificatePEM != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(repository.CACertificatePEM)) {
				return nil, unreachable("repository CA certificate is not valid PEM")
			}
			tlsConfig.RootCAs = pool
		}
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{
		Transport: transport,
		Timeout:   upstreamRequestTimeout,
		// A repository that redirects is followed, but not into a different
		// scheme: an https repository whose index redirects to http would hand
		// the credential to a plaintext hop.
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if len(via) > 0 && via[0].URL.Scheme == "https" &&
				request.URL.Scheme != "https" {
				return errors.New("refusing redirect from https to http")
			}
			return nil
		},
	}, nil
}

// redactError keeps a credential out of a message. Go's url.Error prints the
// URL it was given, and the credential is never in the URL — but a repository
// that redirects can put one there, so the userinfo is removed rather than
// trusted to be absent.
func redactError(err error) string {
	message := err.Error()
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.URL != "" {
		if parsed, parseErr := url.Parse(urlError.URL); parseErr == nil &&
			parsed.User != nil {
			redacted := *parsed
			redacted.User = url.User("redacted")
			message = strings.ReplaceAll(message, urlError.URL, redacted.String())
		}
	}
	const maximum = 512
	if len(message) > maximum {
		return message[:maximum] + "…"
	}
	return message
}

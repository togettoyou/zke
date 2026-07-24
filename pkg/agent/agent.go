package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if ctx.Err() != nil {
		return nil
	}
	kubernetesConfig, err := loadKubernetesConfig(cfg.KubeconfigFile)
	if err != nil {
		return err
	}
	kubernetesConfig.UserAgent = "zke-agent/" + agentVersion()
	kubernetesClient, err := kubernetes.NewForConfig(kubernetesConfig)
	if err != nil {
		return errors.New("create Kubernetes client")
	}

	store := NewIdentityStore(
		kubernetesClient,
		cfg.IdentityNamespace,
		cfg.IdentitySecretName,
	)
	state, err := store.LoadOrCreatePending(ctx)
	if err != nil {
		return err
	}
	var identity LocalIdentity
	version := agentVersion()
	if state.Identity != nil {
		identity = *state.Identity
	} else {
		token, err := readEnrollmentToken(cfg.EnrollmentTokenFile)
		if err != nil {
			return err
		}
		client, err := newRegistrationClient(cfg)
		if err != nil {
			return err
		}
		identity, err = enrollWithRetry(
			ctx,
			cfg,
			logger,
			store,
			client,
			token,
			*state.Pending,
			version,
		)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
	logger.Info(
		"Agent identity ready",
		slog.String("cluster_id", identity.ClusterID),
		slog.String("agent_id", identity.AgentID),
		slog.Time("certificate_expires_at", identity.CertificateExpiresAt),
	)
	err = runConnectionLoop(
		ctx,
		cfg,
		identity,
		version,
		logger,
	)
	logger.Info("agent stopped")
	return err
}

func enrollWithRetry(
	ctx context.Context,
	cfg Config,
	logger *slog.Logger,
	store *IdentityStore,
	client *registrationClient,
	token string,
	pending PendingIdentity,
	version string,
) (LocalIdentity, error) {
	interval := cfg.RetryInitialInterval
	for {
		registration, err := client.Enroll(
			ctx,
			token,
			pending,
			version,
		)
		if err == nil {
			return store.Complete(ctx, pending, registration, time.Now().UTC())
		}
		if ctx.Err() != nil {
			return LocalIdentity{}, nil
		}
		retry, retryAfter := registrationRetry(err)
		if !retry {
			return LocalIdentity{}, err
		}
		delay := retryDelay(interval, retryAfter, cfg.RetryMaxInterval)
		logger.Warn(
			"Agent enrollment will be retried",
			slog.String("error", err.Error()),
			slog.Duration("retry_after", delay),
		)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return LocalIdentity{}, nil
		case <-timer.C:
		}
		interval = min(interval*2, cfg.RetryMaxInterval)
	}
}

func registrationRetry(err error) (bool, time.Duration) {
	var responseError *registrationError
	if errors.As(err, &responseError) {
		return responseError.retryable, responseError.retryAfter
	}
	var decodeError *registrationResponseError
	if errors.As(err, &decodeError) {
		return decodeError.retryable, 0
	}
	if permanentTLSRegistrationError(err) {
		return false, 0
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		if errors.Is(requestError.Err, io.EOF) ||
			errors.Is(requestError.Err, io.ErrUnexpectedEOF) ||
			errors.Is(requestError.Err, context.DeadlineExceeded) {
			return true, 0
		}
		var underlyingNetworkError net.Error
		if errors.As(requestError.Err, &underlyingNetworkError) {
			return true, 0
		}
		return false, 0
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true, 0
	}
	return false, 0
}

func permanentTLSRegistrationError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var recordHeaderError tls.RecordHeaderError
	return errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameError) ||
		errors.As(err, &invalidCertificate) ||
		errors.As(err, &recordHeaderError)
}

func retryDelay(
	interval time.Duration,
	retryAfter time.Duration,
	maximum time.Duration,
) time.Duration {
	delay := max(interval, retryAfter)
	jitter := 1 + rand.Float64()*0.2
	return min(time.Duration(float64(delay)*jitter), maximum)
}

func agentVersion() string {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	version := strings.TrimSpace(buildInfo.Main.Version)
	if version == "" || version == "(devel)" || len(version) > 128 {
		return "development"
	}
	return version
}

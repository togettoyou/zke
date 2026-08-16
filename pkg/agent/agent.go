package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/togettoyou/zke/pkg/shared/buildinfo"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"k8s.io/client-go/dynamic"
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
	kubernetesConfig.UserAgent = "zke-agent/" + buildinfo.Version()
	if kubernetesConfig.Timeout == 0 ||
		kubernetesConfig.Timeout > cfg.Connection.MaxResourceRequestTimeout {
		kubernetesConfig.Timeout = cfg.Connection.MaxResourceRequestTimeout
	}
	kubernetesClient, err := kubernetes.NewForConfig(kubernetesConfig)
	if err != nil {
		return errors.New("create Kubernetes typed client")
	}
	dynamicClient, err := dynamic.NewForConfig(kubernetesConfig)
	if err != nil {
		return errors.New("create Kubernetes dynamic client")
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
	if err := loadTrust(ctx, kubernetesClient, &cfg); err != nil {
		return err
	}
	var identity LocalIdentity
	version := buildinfo.Version()
	if state.Identity != nil {
		identity = *state.Identity
	} else {
		token, err := loadEnrollmentToken(
			ctx,
			kubernetesClient,
			cfg.IdentityNamespace,
		)
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
	go runTerminalSessionJanitor(ctx, kubernetesClient, cfg.IdentityNamespace, logger)
	metricsForwarder := startMetricsIngest(ctx, cfg, kubernetesClient, logger)
	startupID, err := identifier.NewUUID()
	if err != nil {
		return fmt.Errorf("generate Agent startup identifier: %w", err)
	}
	err = runConnectionLoopWithServices(
		ctx,
		cfg,
		store,
		identity,
		version,
		startupID,
		logger,
		connectionServices{
			resourceHandler: newKubernetesResourceHandler(
				dynamicClient,
				kubernetesClient.Discovery(),
				cfg.Connection.MaxResourceBodyBytes,
				cfg.IdentityNamespace,
			),
			podLogsHandler:         newKubernetesPodLogsHandler(kubernetesClient),
			podExecHandler:         newKubernetesPodExecHandler(kubernetesClient, kubernetesConfig),
			podPortForwardHandler:  newKubernetesPodPortForwardHandler(kubernetesClient, kubernetesConfig),
			resourceWatchHandler:   newKubernetesResourceWatchHandler(kubernetesClient),
			terminalSessionHandler: newKubernetesTerminalSessionHandler(kubernetesClient, cfg.IdentityNamespace),
			metricsForwarder:       metricsForwarder,
			metricsCollectorHandler: newKubernetesMetricsCollectorHandler(
				kubernetesClient,
				collectorPlacement{
					Namespace:     cfg.IdentityNamespace,
					AdvertisedURL: cfg.MetricsIngest.AdvertisedURL,
					// The variable every Pod gets and nothing else does. It is
					// what client-go itself reads to decide the same question.
					InCluster: os.Getenv("KUBERNETES_SERVICE_HOST") != "",
				},
			),
		},
	)
	logger.Info("agent stopped")
	return err
}

// startMetricsIngest brings up the collector endpoint.
//
// It runs whether or not a collector is installed. Before an install there is
// no credential, so the endpoint authorizes nobody; after one, the credential
// appears in the Secret the Agent itself wrote, and the refresh below picks it
// up without a restart. A missing Secret is therefore the normal starting
// state rather than a fault.
func startMetricsIngest(
	ctx context.Context,
	cfg Config,
	kubernetesClient kubernetes.Interface,
	logger *slog.Logger,
) *metricsIngestForwarder {
	tokens := newMetricsIngestTokens(kubernetesClient, cfg.IdentityNamespace)
	if err := tokens.refresh(ctx); err != nil {
		logger.Debug(
			"Agent metrics ingest credential is not present yet",
			slog.String("error", err.Error()),
		)
	}
	forwarder := newMetricsIngestForwarder(cfg.MetricsIngest, tokens, logger)
	go runMetricsIngestTokenRefresh(
		ctx,
		tokens,
		cfg.MetricsIngest.TokenRefreshInterval,
		logger,
	)
	go func() {
		if err := runMetricsIngestEndpoint(
			ctx,
			cfg.MetricsIngest,
			forwarder,
			logger,
		); err != nil && ctx.Err() == nil {
			logger.Error(
				"Agent metrics ingest endpoint stopped",
				slog.String("error", err.Error()),
			)
		}
	}()
	return forwarder
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
	interval := cfg.Registration.RetryInitialInterval
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
		delay := retryDelay(
			interval,
			retryAfter,
			cfg.Registration.RetryMaxInterval,
		)
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
		interval = min(interval*2, cfg.Registration.RetryMaxInterval)
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

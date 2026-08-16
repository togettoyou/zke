package agent

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/observability"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// The Agent generates this credential when it installs the collector, so
	// it can insist on a length no hand-typed value would have.
	minMetricsIngestTokenLength = 32
	metricsIngestCollector      = observability.CollectorContainerName
)

var (
	errMetricsIngestUnavailable = errors.New("Agent has no Server connection for metrics")
	errMetricsIngestDisabled    = errors.New("Server does not accept metrics from this Agent")
)

// metricsIngestForwarder bridges the in-cluster collector to the Server. It
// holds no queue of its own: the collector already owns a durable buffer, and
// a second one here would only add a place for samples to be lost when the
// Agent Pod restarts.
type metricsIngestForwarder struct {
	config MetricsIngestConfig
	logger *slog.Logger
	tokens *metricsIngestTokens

	// admissions bounds how many payloads may be held in memory at once. The
	// collector retries what it cannot hand over.
	admissions chan struct{}

	mutex      sync.Mutex
	connection *quic.Conn
	session    *agentprotocol.MetricsIngestSession
}

func newMetricsIngestForwarder(
	config MetricsIngestConfig,
	tokens *metricsIngestTokens,
	logger *slog.Logger,
) *metricsIngestForwarder {
	return &metricsIngestForwarder{
		config:     config,
		logger:     logger,
		tokens:     tokens,
		admissions: make(chan struct{}, max(1, config.MaxConcurrentBatches)),
	}
}

// attach publishes the Connection that new ingest Streams are opened on. It is
// called once per established Connection, after capability negotiation.
func (forwarder *metricsIngestForwarder) attach(connection *quic.Conn) {
	forwarder.mutex.Lock()
	defer forwarder.mutex.Unlock()
	forwarder.connection = connection
	forwarder.session = nil
}

// detach stops accepting new batches until another Connection is attached.
func (forwarder *metricsIngestForwarder) detach() {
	forwarder.mutex.Lock()
	session := forwarder.session
	forwarder.connection = nil
	forwarder.session = nil
	forwarder.mutex.Unlock()
	// The Connection is going away with its Streams; ending the session
	// explicitly keeps the Server from waiting out the Stream deadline.
	session.Abort()
}

func (forwarder *metricsIngestForwarder) forward(
	ctx context.Context,
	payload []byte,
) (*agentv1.MetricsIngestAck, error) {
	ack, err := forwarder.attempt(ctx, payload)
	if err == nil || ctx.Err() != nil {
		return ack, err
	}
	// One retry on a fresh session. A session ends on its own deadline as well
	// as on failure, and the request that discovers it should not be the one
	// to pay for it. Re-sending is safe: a sample carries its own timestamp,
	// so a batch the Server already stored is written again to the same
	// series and position rather than duplicated.
	forwarder.logger.Debug(
		"metrics batch will be retried on a new ingest session",
		slog.String("error", err.Error()),
	)
	return forwarder.attempt(ctx, payload)
}

func (forwarder *metricsIngestForwarder) attempt(
	ctx context.Context,
	payload []byte,
) (*agentv1.MetricsIngestAck, error) {
	session, err := forwarder.acquire(ctx)
	if err != nil {
		return nil, err
	}
	ack, err := session.Send(ctx, payload)
	if err != nil {
		forwarder.discard(session)
		return nil, err
	}
	return ack, nil
}

func (forwarder *metricsIngestForwarder) acquire(
	ctx context.Context,
) (*agentprotocol.MetricsIngestSession, error) {
	forwarder.mutex.Lock()
	defer forwarder.mutex.Unlock()
	if forwarder.session != nil {
		return forwarder.session, nil
	}
	if forwarder.connection == nil {
		return nil, errMetricsIngestUnavailable
	}
	requestID, err := identifier.NewUUID()
	if err != nil {
		return nil, err
	}
	session, err := agentprotocol.OpenMetricsIngest(
		ctx,
		forwarder.connection,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_METRICS_INGEST,
			RequestId:       requestID,
			TimeoutMillis:   uint64(forwarder.config.SessionTimeout.Milliseconds()),
		},
		metricsIngestCollector,
		forwarder.config.MaxBatchBytes,
	)
	if err != nil {
		return nil, err
	}
	forwarder.session = session
	return session, nil
}

func (forwarder *metricsIngestForwarder) discard(
	session *agentprotocol.MetricsIngestSession,
) {
	forwarder.mutex.Lock()
	defer forwarder.mutex.Unlock()
	// Only clear the session this caller was using. A concurrent request may
	// already have opened its replacement.
	if forwarder.session == session {
		forwarder.session = nil
	}
}

// ServeHTTP accepts one remote write request from the in-cluster collector.
// The endpoint is reachable by anything inside the Cluster, so it
// authenticates every request; the token only keeps foreign writers out and
// never establishes which Cluster the data belongs to. That is decided by the
// Server from the Agent's mTLS identity.
func (forwarder *metricsIngestForwarder) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.Path != observability.IngestWritePath {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !forwarder.tokens.authorize(request.Header.Get("Authorization")) {
		// No detail: a caller that guessed a wrong token learns only that it
		// was wrong.
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !strings.EqualFold(request.Header.Get("Content-Encoding"), "snappy") {
		http.Error(
			writer,
			"snappy content encoding is required",
			http.StatusUnsupportedMediaType,
		)
		return
	}
	// Remote write 2.0 reuses the same endpoint with a different message
	// schema. Accepting it here would hand the Server a payload its scope
	// rewrite cannot read, so it is refused at the door.
	if version := request.Header.Get("X-Prometheus-Remote-Write-Version"); version != "" &&
		!strings.HasPrefix(version, "0.1") {
		http.Error(
			writer,
			"only Prometheus remote write 1.0 is supported",
			http.StatusUnsupportedMediaType,
		)
		return
	}
	if strings.Contains(request.Header.Get("Content-Type"), "io.prometheus.write.v2.Request") {
		http.Error(
			writer,
			"only Prometheus remote write 1.0 is supported",
			http.StatusUnsupportedMediaType,
		)
		return
	}

	select {
	case forwarder.admissions <- struct{}{}:
		defer func() { <-forwarder.admissions }()
	default:
		retryAfter(writer, time.Second)
		http.Error(writer, "metrics forwarding is busy", http.StatusTooManyRequests)
		return
	}

	limit := int64(forwarder.config.MaxBatchBytes)
	payload, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		http.Error(writer, "request body could not be read", http.StatusBadRequest)
		return
	}
	if int64(len(payload)) > limit {
		http.Error(
			writer,
			"batch exceeds the configured maximum size",
			http.StatusRequestEntityTooLarge,
		)
		return
	}
	if len(payload) == 0 {
		writer.WriteHeader(http.StatusNoContent)
		return
	}

	ack, err := forwarder.forward(request.Context(), payload)
	if err != nil {
		forwarder.writeForwardError(writer, err)
		return
	}
	forwarder.writeAck(writer, ack)
}

func (forwarder *metricsIngestForwarder) writeForwardError(
	writer http.ResponseWriter,
	err error,
) {
	switch {
	case errors.Is(err, errMetricsIngestUnavailable),
		errors.Is(err, agentprotocol.ErrMetricsIngestRejected),
		errors.Is(err, errMetricsIngestDisabled):
		retryAfter(writer, forwarder.config.UnavailableRetryAfter)
		http.Error(
			writer,
			"Agent is not connected to ZKE Server",
			http.StatusServiceUnavailable,
		)
	case errors.Is(err, agentprotocol.ErrMetricsBatchTooLarge):
		http.Error(
			writer,
			"batch exceeds the negotiated maximum size",
			http.StatusRequestEntityTooLarge,
		)
	case errors.Is(err, context.Canceled):
		// The collector went away; nothing useful can be written back.
		writer.WriteHeader(http.StatusServiceUnavailable)
	default:
		forwarder.logger.Warn(
			"metrics batch could not be forwarded",
			slog.String("error", err.Error()),
		)
		retryAfter(writer, forwarder.config.UnavailableRetryAfter)
		http.Error(
			writer,
			"metrics could not be forwarded",
			http.StatusServiceUnavailable,
		)
	}
}

func (forwarder *metricsIngestForwarder) writeAck(
	writer http.ResponseWriter,
	ack *agentv1.MetricsIngestAck,
) {
	switch ack.GetResult() {
	case agentv1.ResultCode_RESULT_CODE_OK:
		writer.WriteHeader(http.StatusNoContent)
	case agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED:
		delay := time.Duration(ack.GetRetryAfterMillis()) * time.Millisecond
		if delay <= 0 {
			delay = forwarder.config.UnavailableRetryAfter
		}
		retryAfter(writer, delay)
		http.Error(writer, "ZKE Server is throttling metrics", http.StatusTooManyRequests)
	case agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT:
		// Retrying an unchanged payload would fail again, so this is reported
		// as a client error without a retry hint.
		http.Error(writer, "ZKE Server rejected the batch", http.StatusBadRequest)
	case agentv1.ResultCode_RESULT_CODE_FORBIDDEN:
		http.Error(writer, "metrics ingest is not permitted", http.StatusForbidden)
	default:
		retryAfter(writer, forwarder.config.UnavailableRetryAfter)
		http.Error(writer, "ZKE Server could not store the batch", http.StatusServiceUnavailable)
	}
}

func retryAfter(writer http.ResponseWriter, delay time.Duration) {
	if delay <= 0 {
		return
	}
	seconds := int(delay.Round(time.Second) / time.Second)
	writer.Header().Set("Retry-After", strconv.Itoa(max(1, seconds)))
}

// metricsIngestTokens holds the credentials the in-cluster collector presents.
// Two values are accepted so a rotation can apply the new Secret before every
// collector Pod has restarted.
type metricsIngestTokens struct {
	client    kubernetes.Interface
	namespace string

	mutex  sync.RWMutex
	values [][]byte
}

func newMetricsIngestTokens(
	client kubernetes.Interface,
	namespace string,
) *metricsIngestTokens {
	return &metricsIngestTokens{client: client, namespace: namespace}
}

func (tokens *metricsIngestTokens) refresh(ctx context.Context) error {
	secret, err := tokens.client.CoreV1().Secrets(tokens.namespace).Get(
		ctx,
		observability.IngestSecretName,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("read Agent metrics ingest Secret: %w", err)
	}
	var values [][]byte
	for _, key := range []string{observability.IngestTokenKey, observability.IngestPreviousTokenKey} {
		value := strings.TrimSpace(string(secret.Data[key]))
		if len(value) < minMetricsIngestTokenLength {
			continue
		}
		values = append(values, []byte(value))
	}
	if len(values) == 0 {
		return errors.New(
			"Agent metrics ingest Secret does not contain a usable token",
		)
	}
	tokens.mutex.Lock()
	tokens.values = values
	tokens.mutex.Unlock()
	return nil
}

func (tokens *metricsIngestTokens) authorize(header string) bool {
	presented, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		return false
	}
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return false
	}
	tokens.mutex.RLock()
	defer tokens.mutex.RUnlock()
	authorized := false
	for _, value := range tokens.values {
		// Every candidate is compared, and the comparison itself is constant
		// time, so neither the outcome nor the number of tokens is visible in
		// how long this takes.
		if subtle.ConstantTimeCompare([]byte(presented), value) == 1 {
			authorized = true
		}
	}
	return authorized
}

// runMetricsIngestEndpoint serves the collector endpoint for the life of ctx.
func runMetricsIngestEndpoint(
	ctx context.Context,
	config MetricsIngestConfig,
	forwarder *metricsIngestForwarder,
	logger *slog.Logger,
) error {
	server := &http.Server{
		Addr:              config.Address,
		Handler:           forwarder,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
		ErrorLog:          nil,
	}
	done := make(chan error, 1)
	go func() {
		logger.Info(
			"Agent metrics ingest endpoint listening",
			slog.String("address", config.Address),
		)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			config.ShutdownTimeout,
		)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		return <-done
	case err := <-done:
		return err
	}
}

// runMetricsIngestTokenRefresh keeps the accepted tokens current so a rotated
// Secret takes effect without restarting the Agent.
func runMetricsIngestTokenRefresh(
	ctx context.Context,
	tokens *metricsIngestTokens,
	interval time.Duration,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := tokens.refresh(ctx); err != nil && ctx.Err() == nil {
				logger.Warn(
					"Agent metrics ingest token could not be refreshed",
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

package metricsingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang/snappy"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const (
	DefaultMaxDecompressedBytes = 32 * 1024 * 1024
	DefaultMaxSeriesPerBatch    = 50_000
	DefaultMaxSamplesPerBatch   = 200_000
	DefaultMaxLabelsPerSeries   = 64
	DefaultMaxLabelNameBytes    = 128
	DefaultMaxLabelValueBytes   = 1024
	DefaultMaxSampleAge         = 24 * time.Hour
	DefaultMaxSampleFuture      = 10 * time.Minute
	DefaultWriteTimeout         = 30 * time.Second
	DefaultRetryAfter           = 5 * time.Second

	// A compressed batch that expands beyond this ratio is treated as hostile
	// rather than merely large: the limit is on the ratio, not only on the
	// output size, so a small payload cannot force a large allocation before
	// the size check has anything to look at.
	maxDecompressionRatio = 64
)

// Config describes where accepted batches go and how large one may be.
type Config struct {
	// WriteURL is the storage backend's Prometheus remote write endpoint. It
	// is reached by the Server only; browsers never see it.
	WriteURL             string
	WriteTimeout         time.Duration
	MaxDecompressedBytes int
	Limits               Limits
	// ClusterLimits bound a Cluster over time rather than per batch. They are
	// separate from Limits because they need state that outlives one batch.
	ClusterLimits ClusterLimits
	HTTPClient    *http.Client
	Now           func() time.Time
}

// Gateway is the only place a Cluster's samples enter ZKE storage. It owns the
// scope rewrite, so every path into storage goes through the same enforcement.
type Gateway struct {
	writeURL             string
	writeTimeout         time.Duration
	maxDecompressedBytes int
	limits               Limits
	clusters             *clusterLimiter
	client               *http.Client
	logger               *slog.Logger
	now                  func() time.Time
}

func New(config Config, logger *slog.Logger) (*Gateway, error) {
	if logger == nil {
		return nil, errors.New("metrics ingest logger is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(config.WriteURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New(
			"metrics storage write URL must be an absolute http or https URL",
		)
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.MaxDecompressedBytes <= 0 {
		config.MaxDecompressedBytes = DefaultMaxDecompressedBytes
	}
	limits := config.Limits
	if limits.MaxSeriesPerBatch <= 0 {
		limits.MaxSeriesPerBatch = DefaultMaxSeriesPerBatch
	}
	if limits.MaxSamplesPerBatch <= 0 {
		limits.MaxSamplesPerBatch = DefaultMaxSamplesPerBatch
	}
	if limits.MaxLabelsPerSeries <= 0 {
		limits.MaxLabelsPerSeries = DefaultMaxLabelsPerSeries
	}
	if limits.MaxLabelNameBytes <= 0 {
		limits.MaxLabelNameBytes = DefaultMaxLabelNameBytes
	}
	if limits.MaxLabelValueBytes <= 0 {
		limits.MaxLabelValueBytes = DefaultMaxLabelValueBytes
	}
	if limits.MaxSampleAge <= 0 {
		limits.MaxSampleAge = DefaultMaxSampleAge
	}
	if limits.MaxSampleFuture <= 0 {
		limits.MaxSampleFuture = DefaultMaxSampleFuture
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: config.WriteTimeout}
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Gateway{
		writeURL:             parsed.String(),
		writeTimeout:         config.WriteTimeout,
		maxDecompressedBytes: config.MaxDecompressedBytes,
		limits:               limits,
		clusters:             newClusterLimiter(config.ClusterLimits, now),
		client:               client,
		logger:               logger,
		now:                  now,
	}, nil
}

// ClusterState reports what the ingest budget knows about one Cluster. The
// second return is false when the Cluster has sent nothing this Server
// remembers, which is not the same as "within budget".
func (gateway *Gateway) ClusterState(clusterID string) (ClusterState, bool) {
	return gateway.clusters.state(clusterID)
}

// IngestMetrics implements agentconn.MetricsSink.
func (gateway *Gateway) IngestMetrics(
	ctx context.Context,
	scope agentconn.MetricsScope,
	payload io.Reader,
	size uint64,
) agentprotocol.MetricsIngestResult {
	compressed, err := readExactly(payload, size)
	if err != nil {
		return result(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			"PayloadTruncated",
			"metrics batch payload was shorter than declared",
			0,
		)
	}
	decompressed, err := gateway.decompress(compressed)
	if err != nil {
		return failure(err)
	}
	rewritten, stats, err := rewriteBatch(
		decompressed,
		[]label{{name: ClusterLabel, value: scope.ClusterID}},
		gateway.limits,
		gateway.now(),
	)
	if err != nil {
		// The reason is safe to return; the message is written by this package
		// and never quotes label values or sample data.
		gateway.logger.Warn(
			"metrics batch rejected",
			slog.String("tenant_id", scope.TenantID),
			slog.String("project_id", scope.ProjectID),
			slog.String("cluster_id", scope.ClusterID),
			slog.String("error", err.Error()),
		)
		return failure(err)
	}
	// Charged after the batch is understood and before anything is written: the
	// counts come from the parse, and a Cluster over budget must not reach
	// storage at all.
	if reason, retry, entered := gateway.clusters.admit(
		scope.ClusterID,
		stats.samples,
		stats.hashes,
	); reason != "" {
		if entered {
			gateway.logger.Warn(
				"cluster metrics ingest throttled",
				slog.String("tenant_id", scope.TenantID),
				slog.String("project_id", scope.ProjectID),
				slog.String("cluster_id", scope.ClusterID),
				slog.String("reason", reason),
				slog.Int("series", stats.series),
				slog.Int("samples", stats.samples),
			)
		}
		return result(
			agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
			throttleResultReason(reason),
			"cluster exceeds its metrics ingest budget",
			uint64(retry.Milliseconds()),
		)
	}
	if err := gateway.forward(ctx, snappy.Encode(nil, rewritten)); err != nil {
		var rejection *backendRejection
		if errors.As(err, &rejection) {
			return rejection.result()
		}
		if errors.Is(err, context.Canceled) {
			return result(
				agentv1.ResultCode_RESULT_CODE_CANCELED,
				"Canceled",
				"metrics write was canceled",
				0,
			)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return result(
				agentv1.ResultCode_RESULT_CODE_TIMEOUT,
				"Timeout",
				"metrics storage did not answer in time",
				0,
			)
		}
		gateway.logger.Warn(
			"metrics storage write failed",
			slog.String("cluster_id", scope.ClusterID),
			slog.String("error", err.Error()),
		)
		return result(
			agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			"StorageUnavailable",
			"metrics storage is unavailable",
			uint64(DefaultRetryAfter.Milliseconds()),
		)
	}
	gateway.logger.Debug(
		"metrics batch accepted",
		slog.String("tenant_id", scope.TenantID),
		slog.String("project_id", scope.ProjectID),
		slog.String("cluster_id", scope.ClusterID),
		slog.Int("series", stats.series),
		slog.Int("samples", stats.samples),
	)
	return agentprotocol.MetricsIngestResult{
		Result: agentv1.ResultCode_RESULT_CODE_OK,
	}
}

func (gateway *Gateway) decompress(compressed []byte) ([]byte, error) {
	size, err := snappy.DecodedLen(compressed)
	if err != nil {
		return nil, fmt.Errorf("%w: payload is not snappy encoded", ErrPayloadInvalid)
	}
	if size > gateway.maxDecompressedBytes {
		return nil, fmt.Errorf(
			"%w: decompressed payload exceeds %d bytes",
			ErrPayloadTooLarge,
			gateway.maxDecompressedBytes,
		)
	}
	if size > len(compressed)*maxDecompressionRatio {
		return nil, fmt.Errorf(
			"%w: payload decompression ratio exceeds %d",
			ErrPayloadTooLarge,
			maxDecompressionRatio,
		)
	}
	decoded, err := snappy.Decode(nil, compressed)
	if err != nil {
		return nil, fmt.Errorf("%w: payload could not be decompressed", ErrPayloadInvalid)
	}
	return decoded, nil
}

func (gateway *Gateway) forward(ctx context.Context, body []byte) error {
	writeContext, cancel := context.WithTimeout(ctx, gateway.writeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		writeContext,
		http.MethodPost,
		gateway.writeURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Content-Encoding", "snappy")
	request.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	response, err := gateway.client.Do(request)
	if err != nil {
		if writeContext.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
		}
		return err
	}
	defer func() {
		// Draining a bounded prefix lets the connection be reused; the body of
		// a write response carries nothing this Server acts on.
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		_ = response.Body.Close()
	}()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return &backendRejection{
		statusCode: response.StatusCode,
		retryAfter: retryAfter(response.Header.Get("Retry-After")),
	}
}

type backendRejection struct {
	statusCode int
	retryAfter time.Duration
}

func (rejection *backendRejection) Error() string {
	return fmt.Sprintf(
		"metrics storage rejected the write with status %d",
		rejection.statusCode,
	)
}

func (rejection *backendRejection) result() agentprotocol.MetricsIngestResult {
	switch {
	case rejection.statusCode == http.StatusTooManyRequests,
		rejection.statusCode == http.StatusServiceUnavailable:
		retry := rejection.retryAfter
		if retry <= 0 {
			retry = DefaultRetryAfter
		}
		return result(
			agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
			"StorageThrottled",
			"metrics storage is throttling writes",
			uint64(retry.Milliseconds()),
		)
	case rejection.statusCode >= 400 && rejection.statusCode < 500:
		// The batch is well formed as far as this Server can tell but the
		// backend refuses it. Replaying it would fail the same way, so no
		// retry hint is offered.
		return result(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			"StorageRejected",
			"metrics storage rejected the batch",
			0,
		)
	default:
		return result(
			agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			"StorageUnavailable",
			"metrics storage is unavailable",
			uint64(DefaultRetryAfter.Milliseconds()),
		)
	}
}

// throttleResultReason keeps the wire reason in the CamelCase shape every other
// result in this package uses, while the Console-facing state keeps the
// snake_case reason it filters on.
func throttleResultReason(reason string) string {
	if reason == ThrottleReasonCardinality {
		return "ClusterCardinalityExceeded"
	}
	return "ClusterSampleRateExceeded"
}

func failure(err error) agentprotocol.MetricsIngestResult {
	if errors.Is(err, ErrPayloadTooLarge) {
		return result(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			"PayloadTooLarge",
			"metrics batch exceeds a configured limit",
			0,
		)
	}
	return result(
		agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
		"PayloadInvalid",
		"metrics batch could not be parsed",
		0,
	)
}

func result(
	code agentv1.ResultCode,
	reason string,
	message string,
	retryAfterMillis uint64,
) agentprotocol.MetricsIngestResult {
	return agentprotocol.MetricsIngestResult{
		Result:           code,
		Reason:           reason,
		Message:          message,
		RetryAfterMillis: retryAfterMillis,
	}
}

func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, time.Hour)
}

func readExactly(reader io.Reader, size uint64) ([]byte, error) {
	if size == 0 {
		return nil, errors.New("metrics batch payload is empty")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

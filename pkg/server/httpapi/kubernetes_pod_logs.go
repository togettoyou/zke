package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/podlogs"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

const podLogTrailerNames = "X-ZKE-Log-Result, X-ZKE-Log-Limit-Reached, X-ZKE-Log-Bytes"

type PodLogsHTTPConfig struct {
	SnapshotTimeout       time.Duration
	MaximumFollowDuration time.Duration
	RevalidateInterval    time.Duration
	WriteTimeout          time.Duration
}

type podLogsService interface {
	Stream(context.Context, podlogs.Input, io.Writer) (podlogs.Result, error)
}

type kubernetesPodLogsHandler struct {
	baseHandler
	service     podLogsService
	authService *auth.Service
	rbacService *rbac.Service
	config      PodLogsHTTPConfig
}

func newKubernetesPodLogsHandler(
	logger *slog.Logger,
	service podLogsService,
	authService *auth.Service,
	rbacService *rbac.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
	config PodLogsHTTPConfig,
) *kubernetesPodLogsHandler {
	if config.SnapshotTimeout <= 0 {
		config.SnapshotTimeout = operationTimeout
	}
	if config.MaximumFollowDuration <= 0 {
		config.MaximumFollowDuration = 30 * time.Minute
	}
	if config.RevalidateInterval <= 0 {
		config.RevalidateInterval = 15 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 5 * time.Second
	}
	return &kubernetesPodLogsHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
		authService: authService,
		rbacService: rbacService,
		config:      config,
	}
}

func (handler *kubernetesPodLogsHandler) stream(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parsePodLogsQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Pod logs query")
		return
	}
	input.ClusterID = c.Param("cluster_id")
	input.Namespace = c.Param("namespace_name")
	input.PodName = c.Param("pod_name")
	identity, _ := httpmiddleware.Identity(c)
	target := fmt.Sprintf(
		"core/v1/pods %s/%s uid:%s logs:%s",
		input.Namespace,
		input.PodName,
		input.PodUID,
		input.Container,
	)
	if handler.service == nil {
		handler.recordPodLogs(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Pod logs are unavailable")
		return
	}

	duration := handler.config.SnapshotTimeout
	if input.Follow {
		duration = handler.config.MaximumFollowDuration
	}
	streamContext, cancelStream := context.WithTimeout(c.Request.Context(), duration)
	accessRevoked := &atomic.Bool{}
	revalidationDone := closedChannel()
	if input.Follow {
		revalidationDone = handler.revalidateFollowAccess(
			streamContext,
			cancelStream,
			c,
			identity,
			accessRevoked,
		)
	}

	writer := newPodLogHTTPWriter(c.Writer, input.Follow, handler.config.WriteTimeout)
	if input.Follow {
		// net/http's Server.WriteTimeout is an absolute per-request deadline.
		// A quiet follow stream could otherwise expire before its first log line;
		// individual writes below install their own short deadline instead.
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
	}
	result, streamErr := handler.service.Stream(streamContext, input, writer)
	cancelStream()
	<-revalidationDone
	if streamErr != nil {
		handler.recordPodLogs(c, identity.User.ID, target, "failed")
		if writer.Started() {
			writer.Finish(podLogStreamResult(streamContext, streamErr, accessRevoked.Load()), result)
			if !errors.Is(streamErr, context.Canceled) {
				handler.logInternal(c, "stream Kubernetes Pod logs", streamErr)
			}
			return
		}
		handler.respondPodLogsError(c, streamErr, accessRevoked.Load())
		return
	}
	writer.Start()
	writer.Finish("succeeded", result)
	handler.recordPodLogs(c, identity.User.ID, target, "succeeded")
}

func parsePodLogsQuery(query url.Values) (podlogs.Input, error) {
	allowed := map[string]struct{}{
		"uid": {}, "container": {}, "follow": {}, "previous": {},
		"tail_lines": {}, "since_seconds": {}, "timestamps": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return podlogs.Input{}, err
	}
	result := podlogs.Input{
		PodUID:    query.Get("uid"),
		Container: query.Get("container"),
	}
	if result.PodUID == "" || result.Container == "" {
		return podlogs.Input{}, errors.New("Pod UID and container are required")
	}
	for _, item := range []struct {
		name   string
		target *bool
	}{
		{"follow", &result.Follow},
		{"previous", &result.Previous},
		{"timestamps", &result.Timestamps},
	} {
		value := query.Get(item.name)
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return podlogs.Input{}, err
		}
		*item.target = parsed
	}
	if value := query.Get("tail_lines"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return podlogs.Input{}, err
		}
		result.TailLines = &parsed
	} else {
		value := int64(200)
		result.TailLines = &value
	}
	if value := query.Get("since_seconds"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return podlogs.Input{}, err
		}
		result.SinceSeconds = &parsed
	}
	return result, nil
}

func (handler *kubernetesPodLogsHandler) revalidateFollowAccess(
	ctx context.Context,
	cancel context.CancelFunc,
	c *gin.Context,
	identity auth.Identity,
	revoked *atomic.Bool,
) <-chan struct{} {
	done := make(chan struct{})
	sessionToken, tokenExists := httpmiddleware.SessionToken(c)
	clusterID := c.Param("cluster_id")
	go func() {
		defer close(done)
		ticker := time.NewTicker(handler.config.RevalidateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !tokenExists || handler.authService == nil || handler.rbacService == nil {
					revoked.Store(true)
					cancel()
					return
				}
				operationContext, cancelOperation := context.WithTimeout(
					rbac.WithoutBindingCache(ctx),
					handler.operationTimeout,
				)
				current, authErr := handler.authService.Authenticate(
					operationContext,
					sessionToken,
					time.Now().UTC(),
				)
				if authErr == nil &&
					(current.SessionID != identity.SessionID || current.User.ID != identity.User.ID) {
					authErr = errors.New("session identity changed")
				}
				var authorizationErr error
				if authErr == nil {
					_, authorizationErr = handler.rbacService.AuthorizeCluster(
						operationContext,
						identity.User.ID,
						rbac.PermissionClusterPodLogsRead,
						clusterID,
					)
				}
				cancelOperation()
				if authErr != nil || authorizationErr != nil {
					revoked.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return done
}

func closedChannel() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (handler *kubernetesPodLogsHandler) respondPodLogsError(
	c *gin.Context,
	err error,
	accessRevoked bool,
) {
	if accessRevoked {
		writeError(c, http.StatusForbidden, "forbidden", "Pod logs access was revoked")
		return
	}
	if errors.Is(err, podlogs.ErrRequestCapacity) {
		c.Header("Retry-After", "1")
	}
	handler.respondError(
		c,
		"stream Kubernetes Pod logs",
		err,
		errorMapping{podlogs.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Pod logs request"},
		errorMapping{podlogs.ErrPodNotFound, http.StatusNotFound, "pod_not_found", "Kubernetes Pod not found"},
		errorMapping{podlogs.ErrPodReplaced, http.StatusConflict, "pod_replaced", "Kubernetes Pod was replaced; refresh before reading logs"},
		errorMapping{podlogs.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", "Cluster Agent is not connected"},
		errorMapping{podlogs.ErrAgentUnsupported, http.StatusServiceUnavailable, "agent_capability_unavailable", "Cluster Agent does not support Pod logs"},
		errorMapping{podlogs.ErrRequestCapacity, http.StatusTooManyRequests, "pod_logs_capacity_exhausted", "Pod logs request capacity is exhausted"},
		errorMapping{podlogs.ErrClusterUnavailable, http.StatusServiceUnavailable, "cluster_api_unavailable", "Kubernetes API is unavailable"},
		errorMapping{podlogs.ErrClusterTimeout, http.StatusGatewayTimeout, "cluster_api_timeout", "Kubernetes Pod logs request timed out"},
		errorMapping{podlogs.ErrClusterUnauthenticated, http.StatusBadGateway, "cluster_api_unauthenticated", "Agent Kubernetes credentials were rejected"},
		errorMapping{podlogs.ErrClusterAccessDenied, http.StatusBadGateway, "cluster_api_forbidden", "Agent is not allowed to read Kubernetes Pod logs"},
		errorMapping{podlogs.ErrUpstreamConflict, http.StatusConflict, "cluster_api_conflict", "Kubernetes Pod logs changed during the request"},
		errorMapping{podlogs.ErrInvalidResponse, http.StatusBadGateway, "invalid_agent_response", "Agent returned an invalid Pod logs response"},
		errorMapping{podlogs.ErrUpstreamFailure, http.StatusBadGateway, "cluster_api_error", "Kubernetes Pod logs request failed"},
	)
}

func podLogStreamResult(
	ctx context.Context,
	err error,
	accessRevoked bool,
) string {
	if accessRevoked {
		return "access_revoked"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, podlogs.ErrClusterTimeout) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "failed"
}

func (handler *kubernetesPodLogsHandler) recordPodLogs(
	c *gin.Context,
	actorUserID string,
	targetName string,
	result string,
) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actorUserID,
		Action:      auditaction.KubernetesPodLogsRead,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  targetName,
		Result:      result,
	})
}

type podLogHTTPWriter struct {
	writer       http.ResponseWriter
	follow       bool
	writeTimeout time.Duration
	started      bool
	bytesSent    uint64
}

func newPodLogHTTPWriter(
	writer http.ResponseWriter,
	follow bool,
	writeTimeout time.Duration,
) *podLogHTTPWriter {
	return &podLogHTTPWriter{writer: writer, follow: follow, writeTimeout: writeTimeout}
}

func (writer *podLogHTTPWriter) Started() bool {
	return writer.started
}

func (writer *podLogHTTPWriter) Start() {
	if writer.started {
		return
	}
	header := writer.writer.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Trailer", podLogTrailerNames)
	if writer.follow {
		header.Set("X-Accel-Buffering", "no")
	}
	writer.writer.WriteHeader(http.StatusOK)
	writer.started = true
}

func (writer *podLogHTTPWriter) Write(data []byte) (int, error) {
	writer.Start()
	controller := http.NewResponseController(writer.writer)
	_ = controller.SetWriteDeadline(time.Now().Add(writer.writeTimeout))
	written, err := writer.writer.Write(data)
	writer.bytesSent += uint64(written)
	if err == nil && writer.follow {
		err = controller.Flush()
	}
	_ = controller.SetWriteDeadline(time.Time{})
	return written, err
}

func (writer *podLogHTTPWriter) Finish(result string, stream podlogs.Result) {
	writer.Start()
	header := writer.writer.Header()
	header.Set("X-ZKE-Log-Result", result)
	header.Set("X-ZKE-Log-Limit-Reached", strconv.FormatBool(stream.LimitReached))
	header.Set("X-ZKE-Log-Bytes", strconv.FormatUint(writer.bytesSent, 10))
}

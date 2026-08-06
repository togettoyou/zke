package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/resourcewatch"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
)

type KubernetesEventsHTTPConfig struct {
	SnapshotTimeout, MaximumFollowDuration, RevalidateInterval, WriteTimeout time.Duration
}

type kubernetesEventWatchService interface {
	Stream(context.Context, resourcewatch.Input, agentprotocol.ResourceWatchSink) (resourcewatch.Result, error)
}

type kubernetesEventsHandler struct {
	baseHandler
	service     kubernetesEventWatchService
	authService *auth.Service
	rbacService *rbac.Service
	config      KubernetesEventsHTTPConfig
}

func newKubernetesEventsHandler(
	logger *slog.Logger, service kubernetesEventWatchService, authService *auth.Service,
	rbacService *rbac.Service, auditService *audit.Service, operationTimeout time.Duration,
	config KubernetesEventsHTTPConfig,
) *kubernetesEventsHandler {
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
	return &kubernetesEventsHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout), service: service,
		authService: authService, rbacService: rbacService, config: config,
	}
}

func (handler *kubernetesEventsHandler) stream(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	input, err := parseKubernetesEventsQuery(c.Request.URL.Query())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Kubernetes Events query")
		return
	}
	input.ClusterID, input.Namespace = c.Param("cluster_id"), c.Param("namespace_name")
	if input.ResourceVersion == "" {
		input.ResourceVersion = c.GetHeader("Last-Event-ID")
	}
	identity, _ := httpmiddleware.Identity(c)
	target := fmt.Sprintf("core/v1/events namespace:%s", input.Namespace)
	if input.ResourceUID != "" {
		target += " resource_uid:" + input.ResourceUID
	}
	if input.ResourceKind != "" {
		target += " resource_kind:" + input.ResourceKind
	}
	if input.ResourceName != "" {
		target += " resource_name:" + input.ResourceName
	}
	if input.EventType != "" {
		target += " type:" + input.EventType
	}
	if input.Reason != "" {
		target += " reason:" + input.Reason
	}
	if handler.service == nil {
		handler.recordEventRead(c, identity.User.ID, target, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes Events are unavailable")
		return
	}
	duration := handler.config.SnapshotTimeout
	if input.Follow {
		duration = handler.config.MaximumFollowDuration
	}
	streamContext, cancelStream := context.WithTimeout(c.Request.Context(), duration)
	defer cancelStream()
	revoked := &atomic.Bool{}
	sink := newKubernetesEventSSESink(c.Writer, httpmiddleware.RequestID(c), handler.config.WriteTimeout)
	revalidationDone := closedChannel()
	if input.Follow {
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
		revalidationDone = handler.revalidateEventAccess(streamContext, cancelStream, c, identity, sink, revoked)
	}
	result, streamErr := handler.service.Stream(streamContext, input, sink)
	cancelStream()
	<-revalidationDone
	if streamErr != nil {
		closeReason := kubernetesEventCloseReason(streamContext, streamErr, revoked.Load())
		handler.recordEventRead(c, identity.User.ID, target,
			kubernetesEventAuditResult(streamContext, closeReason))
		if sink.Started() {
			_ = sink.Close(closeReason, result)
			if !errors.Is(streamErr, context.Canceled) {
				handler.logInternal(c, "stream Kubernetes Events", streamErr)
			}
			return
		}
		handler.respondEventError(c, streamErr, revoked.Load())
		return
	}
	if !sink.Started() {
		handler.respondEventError(c, resourcewatch.ErrInvalidResponse, false)
		return
	}
	closeReason := "completed"
	if result.LimitReached {
		closeReason = "limit_reached"
	}
	_ = sink.Close(closeReason, result)
	handler.recordEventRead(c, identity.User.ID, target, "succeeded")
}

func parseKubernetesEventsQuery(query url.Values) (resourcewatch.Input, error) {
	allowed := map[string]struct{}{
		"follow": {}, "include_initial": {}, "limit": {}, "resource_version": {},
		"resource_uid": {}, "resource_kind": {}, "resource_name": {}, "type": {}, "reason": {},
	}
	if err := validateQueryNames(query, allowed); err != nil {
		return resourcewatch.Input{}, err
	}
	result := resourcewatch.Input{
		IncludeInitial: true, InitialLimit: 100, ResourceVersion: query.Get("resource_version"),
		ResourceUID: query.Get("resource_uid"), ResourceKind: query.Get("resource_kind"),
		ResourceName: query.Get("resource_name"), EventType: query.Get("type"), Reason: query.Get("reason"),
	}
	for _, item := range []struct {
		name   string
		target *bool
	}{
		{"follow", &result.Follow}, {"include_initial", &result.IncludeInitial},
	} {
		if value := query.Get(item.name); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return resourcewatch.Input{}, err
			}
			*item.target = parsed
		}
	}
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return resourcewatch.Input{}, err
		}
		result.InitialLimit = uint32(parsed)
	}
	result.AllowBookmarks = result.Follow
	return result, nil
}

func (handler *kubernetesEventsHandler) revalidateEventAccess(
	ctx context.Context, cancel context.CancelFunc, c *gin.Context, identity auth.Identity,
	sink *kubernetesEventSSESink, revoked *atomic.Bool,
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
				opCtx, cancelOp := context.WithTimeout(rbac.WithoutBindingCache(ctx), handler.operationTimeout)
				current, authErr := handler.authService.Authenticate(opCtx, sessionToken, time.Now().UTC())
				if authErr == nil && (current.SessionID != identity.SessionID || current.User.ID != identity.User.ID) {
					authErr = errors.New("session identity changed")
				}
				var authorizationErr error
				if authErr == nil {
					_, authorizationErr = handler.rbacService.AuthorizeCluster(opCtx, identity.User.ID, rbac.PermissionClusterEventRead, clusterID)
				}
				cancelOp()
				if authErr != nil || authorizationErr != nil {
					revoked.Store(true)
					cancel()
					return
				}
				if sink.Started() && sink.Comment("heartbeat") != nil {
					cancel()
					return
				}
			}
		}
	}()
	return done
}

func (handler *kubernetesEventsHandler) respondEventError(c *gin.Context, err error, revoked bool) {
	if revoked {
		writeError(c, http.StatusForbidden, "forbidden", "Kubernetes Event access was revoked")
		return
	}
	if errors.Is(err, resourcewatch.ErrRequestCapacity) {
		c.Header("Retry-After", "1")
	}
	handler.respondError(c, "stream Kubernetes Events", err,
		errorMapping{resourcewatch.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Kubernetes Event request"},
		errorMapping{resourcewatch.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", "Cluster Agent is not connected"},
		errorMapping{resourcewatch.ErrAgentUnsupported, http.StatusServiceUnavailable, "agent_capability_unavailable", "Cluster Agent does not support Kubernetes Event watch"},
		errorMapping{resourcewatch.ErrRequestCapacity, http.StatusTooManyRequests, "resource_watch_capacity_exhausted", "Resource Watch request capacity is exhausted"},
		errorMapping{resourcewatch.ErrClusterUnavailable, http.StatusServiceUnavailable, "cluster_api_unavailable", "Kubernetes API is unavailable"},
		errorMapping{resourcewatch.ErrClusterTimeout, http.StatusGatewayTimeout, "cluster_api_timeout", "Kubernetes Event request timed out"},
		errorMapping{resourcewatch.ErrClusterUnauthenticated, http.StatusBadGateway, "cluster_api_unauthenticated", "Agent Kubernetes credentials were rejected"},
		errorMapping{resourcewatch.ErrClusterAccessDenied, http.StatusBadGateway, "cluster_api_forbidden", "Agent is not allowed to read Kubernetes Events"},
		errorMapping{resourcewatch.ErrResourceVersionExpired, http.StatusConflict, "resource_version_expired", "Kubernetes Event resource version expired; request a new snapshot"},
		errorMapping{resourcewatch.ErrWatchClosed, http.StatusServiceUnavailable, "watch_closed", "Kubernetes Event watch closed; reconnect from the last resource version"},
		errorMapping{resourcewatch.ErrInvalidResponse, http.StatusBadGateway, "invalid_agent_response", "Agent returned an invalid Resource Watch response"},
		errorMapping{resourcewatch.ErrUpstreamFailure, http.StatusBadGateway, "cluster_api_error", "Kubernetes Event watch failed"},
	)
}

func kubernetesEventCloseReason(ctx context.Context, err error, revoked bool) string {
	if revoked {
		return "access_revoked"
	}
	if errors.Is(err, resourcewatch.ErrResourceVersionExpired) {
		return "resource_version_expired"
	}
	if errors.Is(err, resourcewatch.ErrWatchClosed) {
		return "watch_closed"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, resourcewatch.ErrClusterTimeout) {
		return "maximum_duration"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, resourcewatch.ErrRequestCapacity) {
		return "capacity_exhausted"
	}
	return "failed"
}

// kubernetesEventAuditResult says how a stream ended in the audit's vocabulary,
// which is not the same question as why it ended.
//
// A followed stream is expected to end. The operator closes the page, the
// Server reaches its own maximum duration, the upstream Watch rotates, the
// resourceVersion ages out — the Console resumes from the last three on its own,
// and none of them means the read failed. Recorded as failures, they buried the
// events page under one failed audit event per reconnection and per visit,
// which is exactly the noise that hides a read that really was refused.
//
// Revocation keeps a result of its own: that stream was cut because the identity
// lost the permission it was reading under, which is a denial and belongs with
// the other denials.
func kubernetesEventAuditResult(ctx context.Context, closeReason string) string {
	switch closeReason {
	case "access_revoked":
		return "denied"
	case "canceled", "watch_closed", "resource_version_expired":
		return "succeeded"
	case "maximum_duration":
		// The Server's own follow deadline and an upstream timeout both surface
		// as ErrClusterTimeout, so the context is what tells them apart: only
		// the deadline this handler set is an ordinary end.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "succeeded"
		}
		return "failed"
	default:
		return "failed"
	}
}

func (handler *kubernetesEventsHandler) recordEventRead(c *gin.Context, actor, target, result string) {
	handler.recordOperation(c, auditedOperation{Scope: auditScopeCluster, ActorUserID: actor,
		Action: auditaction.KubernetesEventRead, TargetType: auditaction.TargetKubernetesResource,
		TargetName: target, Result: result})
}

type kubernetesEventSSESink struct {
	writer              http.ResponseWriter
	requestID           string
	writeTimeout        time.Duration
	mu                  sync.Mutex
	started             bool
	eventsSent          uint64
	bytesSent           uint64
	lastResourceVersion string
}

func newKubernetesEventSSESink(writer http.ResponseWriter, requestID string, timeout time.Duration) *kubernetesEventSSESink {
	return &kubernetesEventSSESink{writer: writer, requestID: requestID, writeTimeout: timeout}
}

func (sink *kubernetesEventSSESink) Started() bool {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.started
}

func (sink *kubernetesEventSSESink) Start(response *agentv1.ResourceWatchResponse) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.started {
		return agentprotocol.ErrStreamProtocol
	}
	header := sink.writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	sink.writer.WriteHeader(http.StatusOK)
	sink.started = true
	sink.lastResourceVersion = response.GetResourceVersion()
	return sink.writeLocked("", "ready", map[string]any{
		"request_id": sink.requestID, "resource_version": response.GetResourceVersion(),
		"initial_events_truncated": response.GetInitialEventsTruncated(),
	})
}

func (sink *kubernetesEventSSESink) Event(frame *agentv1.ResourceWatchEvent) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !sink.started {
		return agentprotocol.ErrStreamProtocol
	}
	eventName := "bookmark"
	var data any = map[string]string{"resource_version": frame.GetResourceVersion()}
	if frame.GetType() != agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_BOOKMARK {
		var event corev1.Event
		if err := json.Unmarshal(frame.GetObject(), &event); err != nil {
			return fmt.Errorf("decode Kubernetes Event: %w", err)
		}
		eventName = "kubernetes.event"
		data = responseKubernetesEvent(frame.GetType(), event)
	}
	if err := sink.writeLocked(frame.GetResourceVersion(), eventName, data); err != nil {
		return err
	}
	sink.eventsSent++
	sink.bytesSent += uint64(len(frame.GetObject()))
	if frame.GetResourceVersion() != "" {
		sink.lastResourceVersion = frame.GetResourceVersion()
	}
	return nil
}

func (sink *kubernetesEventSSESink) Comment(value string) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	controller := http.NewResponseController(sink.writer)
	_ = controller.SetWriteDeadline(time.Now().Add(sink.writeTimeout))
	_, err := fmt.Fprintf(sink.writer, ": %s\n\n", value)
	if err == nil {
		err = controller.Flush()
	}
	_ = controller.SetWriteDeadline(time.Time{})
	return err
}

func (sink *kubernetesEventSSESink) Close(reason string, result resourcewatch.Result) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	lastVersion := sink.lastResourceVersion
	if result.LastResourceVersion != "" {
		lastVersion = result.LastResourceVersion
	}
	return sink.writeLocked(lastVersion, "close", map[string]any{
		"reason": reason, "events_sent": sink.eventsSent, "bytes_sent": sink.bytesSent,
		"last_resource_version": lastVersion, "limit_reached": result.LimitReached,
	})
}

func (sink *kubernetesEventSSESink) writeLocked(id, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	controller := http.NewResponseController(sink.writer)
	_ = controller.SetWriteDeadline(time.Now().Add(sink.writeTimeout))
	if id != "" {
		if _, err = fmt.Fprintf(sink.writer, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(sink.writer, "event: %s\ndata: %s\n\n", event, payload); err == nil {
		err = controller.Flush()
	}
	_ = controller.SetWriteDeadline(time.Time{})
	return err
}

type kubernetesEventResponse struct {
	WatchType           string                  `json:"watch_type"`
	UID                 string                  `json:"uid"`
	Name                string                  `json:"name"`
	Namespace           string                  `json:"namespace"`
	Type                string                  `json:"type"`
	Reason              string                  `json:"reason"`
	Message             string                  `json:"message"`
	Count               int32                   `json:"count"`
	Action              string                  `json:"action,omitempty"`
	ReportingController string                  `json:"reporting_controller,omitempty"`
	ReportingInstance   string                  `json:"reporting_instance,omitempty"`
	Regarding           corev1.ObjectReference  `json:"regarding"`
	Related             *corev1.ObjectReference `json:"related,omitempty"`
	FirstTimestamp      *time.Time              `json:"first_timestamp,omitempty"`
	LastTimestamp       *time.Time              `json:"last_timestamp,omitempty"`
	EventTime           *time.Time              `json:"event_time,omitempty"`
}

func responseKubernetesEvent(watchType agentv1.ResourceWatchEventType, event corev1.Event) kubernetesEventResponse {
	result := kubernetesEventResponse{WatchType: responseWatchType(watchType), UID: string(event.UID), Name: event.Name,
		Namespace: event.Namespace, Type: event.Type, Reason: event.Reason, Message: event.Message,
		Count: event.Count, Action: event.Action, ReportingController: event.ReportingController,
		ReportingInstance: event.ReportingInstance, Regarding: event.InvolvedObject, Related: event.Related}
	if !event.FirstTimestamp.IsZero() {
		value := responseTime(event.FirstTimestamp.Time)
		result.FirstTimestamp = &value
	}
	if !event.LastTimestamp.IsZero() {
		value := responseTime(event.LastTimestamp.Time)
		result.LastTimestamp = &value
	}
	if !event.EventTime.IsZero() {
		value := responseTime(event.EventTime.Time)
		result.EventTime = &value
	}
	return result
}

func responseWatchType(watchType agentv1.ResourceWatchEventType) string {
	switch watchType {
	case agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED:
		return "ADDED"
	case agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_MODIFIED:
		return "MODIFIED"
	case agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_DELETED:
		return "DELETED"
	default:
		return "BOOKMARK"
	}
}

package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/metricscollector"
)

type metricsCollectorService interface {
	Status(context.Context, string) (metricscollector.State, error)
	Details(context.Context, string) (metricscollector.State, error)
	Install(context.Context, string) (metricscollector.State, error)
	Uninstall(context.Context, string) (metricscollector.State, error)
}

// metricsCollectorServiceOrNil keeps a nil *metricscollector.Service from
// becoming a non-nil interface holding a nil pointer, which would turn "this
// deployment stores no metrics" into a nil dereference on the first request.
func metricsCollectorServiceOrNil(
	service *metricscollector.Service,
) metricsCollectorService {
	if service == nil {
		return nil
	}
	return service
}

type metricsCollectorHandler struct {
	baseHandler
	service metricsCollectorService
}

func newMetricsCollectorHandler(
	logger *slog.Logger,
	auditService *audit.Service,
	service metricsCollectorService,
	operationTimeout time.Duration,
) *metricsCollectorHandler {
	return &metricsCollectorHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

type metricsCollectorStateResponse struct {
	ClusterID       string `json:"cluster_id"`
	Installed       bool   `json:"installed"`
	Namespace       string `json:"namespace"`
	Image           string `json:"image"`
	DesiredImage    string `json:"desired_image"`
	DesiredReplicas int32  `json:"desired_replicas"`
	ReadyReplicas   int32  `json:"ready_replicas"`
	CredentialReady bool   `json:"credential_ready"`
	// The fields below describe the Server side of the same link: whether this
	// Server is refusing what the collector sends. Nulls mean the Server has no
	// budget state for the Cluster, which is not the same as "within budget".
	Throttled       bool       `json:"throttled"`
	ThrottleReason  string     `json:"throttle_reason"`
	ThrottledSince  *time.Time `json:"throttled_since"`
	LastThrottledAt *time.Time `json:"last_throttled_at"`
	// ActiveSeries is an estimate from a fixed-size sketch, never an exact
	// count. Zero max means the Server has nothing to report.
	ActiveSeries    int `json:"active_series"`
	MaxActiveSeries int `json:"max_active_series"`
	// One entry per workload the install puts into the Cluster, the collector
	// included. An Agent too old to report them answers with an empty list.
	Components          []metricsComponentStateResponse `json:"components"`
	ScrapeJobs          []metricsScrapeJobResponse      `json:"scrape_jobs"`
	ScrapeJobsTruncated bool                            `json:"scrape_jobs_truncated"`
}

type metricsScrapeJobResponse struct {
	JobName            string   `json:"job_name"`
	SourceKind         string   `json:"source_kind"`
	Namespace          string   `json:"namespace"`
	SourceName         string   `json:"source_name"`
	Scheme             string   `json:"scheme"`
	MetricsPath        string   `json:"metrics_path"`
	Port               string   `json:"port"`
	Authentication     string   `json:"authentication"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify"`
	Targets            []string `json:"targets"`
	TargetsTruncated   bool     `json:"targets_truncated"`
}

type metricsComponentStateResponse struct {
	Component       string `json:"component"`
	Installed       bool   `json:"installed"`
	Image           string `json:"image"`
	DesiredImage    string `json:"desired_image"`
	DesiredReplicas int32  `json:"desired_replicas"`
	ReadyReplicas   int32  `json:"ready_replicas"`
	// Set when the Cluster refused this component rather than when nobody asked
	// for it. The message is written by ZKE and never quotes the Cluster's own
	// error, which can name objects outside this operation.
	UnavailableReason  string `json:"unavailable_reason"`
	UnavailableMessage string `json:"unavailable_message"`
}

// status reports whether the target Cluster is collecting metrics.
//
// It is a read, but it answers from the Cluster rather than from a Server
// record: what matters is what is running there now, and a Server-side flag
// would go stale the moment somebody removed the Deployment by hand.
func (handler *metricsCollectorHandler) status(c *gin.Context) {
	handler.respond(c, "", func(ctx context.Context, clusterID string) (metricscollector.State, error) {
		return handler.service.Status(ctx, clusterID)
	})
}

func (handler *metricsCollectorHandler) details(c *gin.Context) {
	handler.respond(c, "", func(ctx context.Context, clusterID string) (metricscollector.State, error) {
		return handler.service.Details(ctx, clusterID)
	})
}

func (handler *metricsCollectorHandler) install(c *gin.Context) {
	handler.respond(c, auditaction.ClusterMetricsCollectorInstall, func(
		ctx context.Context,
		clusterID string,
	) (metricscollector.State, error) {
		return handler.service.Install(ctx, clusterID)
	})
}

func (handler *metricsCollectorHandler) uninstall(c *gin.Context) {
	handler.respond(c, auditaction.ClusterMetricsCollectorUninstall, func(
		ctx context.Context,
		clusterID string,
	) (metricscollector.State, error) {
		return handler.service.Uninstall(ctx, clusterID)
	})
}

// respond runs one collector operation and records it when it is a mutation.
// A read passes an empty action: status is polled by an open Console window,
// and an audit row per poll would bury the installs among them.
func (handler *metricsCollectorHandler) respond(
	c *gin.Context,
	action string,
	run func(context.Context, string) (metricscollector.State, error),
) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			"metrics collector routes do not accept query parameters",
		)
		return
	}
	clusterID := c.Param("cluster_id")
	identity, _ := httpmiddleware.Identity(c)
	if handler.service == nil {
		if action != "" {
			handler.recordCollectorOperation(c, action, identity.User.ID, clusterID, "failed", "metrics_disabled")
		}
		writeError(
			c,
			http.StatusServiceUnavailable,
			"metrics_disabled",
			"metrics collection is not enabled on this Server",
		)
		return
	}
	ctx, cancel := handler.operationContext(c)
	state, err := run(ctx, clusterID)
	cancel()
	if err != nil {
		if action != "" {
			handler.recordCollectorOperation(c, action, identity.User.ID, clusterID, "failed", collectorFailureReason(err))
		}
		if handler.respondError(
			c,
			"manage metrics collector",
			err,
			errorMapping{
				target:  metricscollector.ErrInvalidInput,
				status:  http.StatusBadRequest,
				code:    "invalid_request",
				message: "指标采集组件配置无效",
			},
			errorMapping{
				target:  agentconn.ErrAgentNotConnected,
				status:  http.StatusServiceUnavailable,
				code:    "agent_unavailable",
				message: "目标集群 Agent 未连接",
			},
			errorMapping{
				target:  metricscollector.ErrUnsupported,
				status:  http.StatusConflict,
				code:    "agent_unsupported",
				message: "目标集群 Agent 版本不支持采集组件管理",
			},
			errorMapping{
				target:  agentconn.ErrMetricsCollectorCapabilityMissing,
				status:  http.StatusConflict,
				code:    "agent_unsupported",
				message: "目标集群 Agent 版本不支持采集组件管理",
			},
			errorMapping{
				target:  metricscollector.ErrRejected,
				status:  http.StatusConflict,
				code:    "collector_rejected",
				message: "目标集群拒绝了该操作",
			},
		) {
			return
		}
		return
	}
	if action != "" {
		handler.recordCollectorOperation(c, action, identity.User.ID, clusterID, "succeeded", "")
	}
	writeSuccess(c, http.StatusOK, metricsCollectorStateResponse{
		ClusterID:           state.ClusterID,
		Installed:           state.Installed,
		Namespace:           state.Namespace,
		Image:               state.Image,
		DesiredImage:        state.DesiredImage,
		DesiredReplicas:     state.DesiredReplicas,
		ReadyReplicas:       state.ReadyReplicas,
		CredentialReady:     state.CredentialReady,
		Throttled:           state.Throttled,
		ThrottleReason:      state.ThrottleReason,
		ThrottledSince:      optionalTime(state.ThrottledSince),
		LastThrottledAt:     optionalTime(state.LastThrottledAt),
		ActiveSeries:        state.ActiveSeries,
		MaxActiveSeries:     state.MaxActiveSeries,
		Components:          componentStatesResponse(state.Components),
		ScrapeJobs:          scrapeJobsResponse(state.ScrapeJobs),
		ScrapeJobsTruncated: state.ScrapeJobsTruncated,
	})
}

func scrapeJobsResponse(jobs []metricscollector.ScrapeJob) []metricsScrapeJobResponse {
	result := make([]metricsScrapeJobResponse, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, metricsScrapeJobResponse{
			JobName:            job.JobName,
			SourceKind:         job.SourceKind,
			Namespace:          job.Namespace,
			SourceName:         job.SourceName,
			Scheme:             job.Scheme,
			MetricsPath:        job.MetricsPath,
			Port:               job.Port,
			Authentication:     job.Authentication,
			InsecureSkipVerify: job.InsecureSkipVerify,
			Targets:            scrapeTargetsResponse(job.Targets),
			TargetsTruncated:   job.TargetsTruncated,
		})
	}
	return result
}

// A built-in Node job has no fixed target list, and an annotated Service with
// no ready backend has none yet. The contract declares targets as an array, so
// both have to serialize as [] — copying with append onto a nil slice would
// leave nil there, and the Console reads its length.
func scrapeTargetsResponse(targets []string) []string {
	result := make([]string, 0, len(targets))
	return append(result, targets...)
}

func componentStatesResponse(
	components []metricscollector.ComponentState,
) []metricsComponentStateResponse {
	result := make([]metricsComponentStateResponse, 0, len(components))
	for _, component := range components {
		result = append(result, metricsComponentStateResponse{
			Component:          component.Component,
			Installed:          component.Installed,
			Image:              component.Image,
			DesiredImage:       component.DesiredImage,
			DesiredReplicas:    component.DesiredReplicas,
			ReadyReplicas:      component.ReadyReplicas,
			UnavailableReason:  component.UnavailableReason,
			UnavailableMessage: component.UnavailableMessage,
		})
	}
	return result
}

// optionalTime keeps a zero time out of the response. Serialising it would
// present year one as a real moment a Cluster was throttled.
func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func (handler *metricsCollectorHandler) recordCollectorOperation(
	c *gin.Context,
	action string,
	actorUserID string,
	clusterID string,
	result string,
	reason string,
) {
	detail := map[string]string{}
	if reason != "" {
		detail["reason"] = reason
	}
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: actorUserID,
		Action:      action,
		TargetType:  auditaction.TargetCluster,
		TargetID:    clusterID,
		Result:      result,
		Detail:      detail,
	})
}

// collectorFailureReason keeps the audit detail to stable keys. The Agent's own
// message never lands here: it can name Kubernetes objects outside this
// operation.
func collectorFailureReason(err error) string {
	switch {
	case errors.Is(err, metricscollector.ErrInvalidInput):
		return "invalid_request"
	case errors.Is(err, agentconn.ErrAgentNotConnected):
		return "agent_unavailable"
	case errors.Is(err, agentconn.ErrMetricsCollectorCapabilityMissing),
		errors.Is(err, metricscollector.ErrUnsupported):
		return "agent_unsupported"
	case errors.Is(err, metricscollector.ErrRejected):
		return "cluster_rejected"
	default:
		return "operation_failed"
	}
}

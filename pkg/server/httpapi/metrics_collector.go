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
		ClusterID:       state.ClusterID,
		Installed:       state.Installed,
		Namespace:       state.Namespace,
		Image:           state.Image,
		DesiredImage:    state.DesiredImage,
		DesiredReplicas: state.DesiredReplicas,
		ReadyReplicas:   state.ReadyReplicas,
		CredentialReady: state.CredentialReady,
	})
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

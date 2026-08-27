package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/helm"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
)

// Helm release writes.
//
// The read routes next door answer "what is installed here" from Helm's own
// storage. These four change it, and they are a different kind of operation
// from anything else in the container service: one request renders a chart,
// creates or replaces every object an application owns, and rewrites the
// history the `helm` client reads. Nothing about that fits a single-object
// write, which is why it has its own permission, its own audit actions and its
// own Agent Stream.
//
// The permission stack is deliberate and is enforced by the routes rather than
// described here — see routes.go. In short: `cluster.helm.manage` says the
// operator may change releases at all, the object permissions the operation
// actually spends are required on top of it, and `cluster.secret.manage` is
// required because Helm's release storage is a Secret and its values are its
// content. The protected-Namespace rules apply as they do to any other write.
//
// One switch is not the caller's to send. A chart that renders a
// CustomResourceDefinition, a ClusterRole or any other object no Namespace
// contains is refused by the Agent unless the request says the operator was
// authorized for the Cluster as a whole, and this handler decides that from
// `cluster.manage` — never from the request body.

const maxHelmReleaseRequestBytes = 2 * 1024 * 1024

type helmReleaseWriteService interface {
	Install(context.Context, helm.InstallInput) (helmrelease.Report, error)
	Upgrade(context.Context, helm.UpgradeInput) (helmrelease.Report, error)
	Rollback(context.Context, helm.RollbackInput) (helmrelease.Report, error)
	Uninstall(context.Context, helm.UninstallInput) (helmrelease.Report, error)
}

func clusterAuthorizerOrNil(service *rbac.Service) clusterAuthorizer {
	if service == nil {
		return nil
	}
	return service
}

func helmServiceOrNil(service *helm.Service) helmReleaseWriteService {
	if service == nil {
		return nil
	}
	return service
}

// clusterAuthorizer is the one authorization question this handler asks for
// itself, narrowed to a method so the decision can be exercised without a
// database behind it.
type clusterAuthorizer interface {
	AuthorizeCluster(
		ctx context.Context,
		userID string,
		permission rbac.Permission,
		clusterID string,
	) (rbac.ResolvedScope, error)
}

type helmReleaseWriteHandler struct {
	baseHandler
	service helmReleaseWriteService
	rbac    clusterAuthorizer
}

func newHelmReleaseWriteHandler(
	logger *slog.Logger,
	service helmReleaseWriteService,
	rbacService clusterAuthorizer,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *helmReleaseWriteHandler {
	return &helmReleaseWriteHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
		rbac:        rbacService,
	}
}

// chartReleaseRequest is what install and upgrade have in common: what to
// install, with which values, and how the operation should run.
//
// The chart is named by repository, name and version rather than by URL: an
// operator installs from the catalogue a platform administrator curated, and a
// request naming an arbitrary address would make this Server fetch from
// wherever the caller liked.
//
// The release name is deliberately not here. An install names it in the body
// and an upgrade names it in the URL; a field that existed in both would make
// "which one wins" a question somebody has to answer, and the answer would be
// wrong somewhere.
type chartReleaseRequest struct {
	RepositoryID    string `json:"repository_id"`
	Chart           string `json:"chart"`
	Version         string `json:"version"`
	Values          string `json:"values"`
	CreateNamespace bool   `json:"create_namespace"`
	Wait            bool   `json:"wait"`
	Atomic          bool   `json:"atomic"`
	DisableHooks    bool   `json:"disable_hooks"`
	TimeoutSeconds  uint32 `json:"timeout_seconds"`
	MaxHistory      uint32 `json:"max_history"`
	Description     string `json:"description"`
	DryRun          bool   `json:"dry_run"`
	Confirm         bool   `json:"confirm"`
}

type installReleaseRequest struct {
	chartReleaseRequest
	Name string `json:"name"`
}

type upgradeReleaseRequest struct {
	chartReleaseRequest
	ResetValues bool `json:"reset_values"`
	ReuseValues bool `json:"reuse_values"`
}

type rollbackReleaseRequest struct {
	Revision       int64  `json:"revision"`
	Wait           bool   `json:"wait"`
	DisableHooks   bool   `json:"disable_hooks"`
	TimeoutSeconds uint32 `json:"timeout_seconds"`
	MaxHistory     uint32 `json:"max_history"`
	Description    string `json:"description"`
	DryRun         bool   `json:"dry_run"`
	Confirm        bool   `json:"confirm"`
}

type uninstallReleaseRequest struct {
	KeepHistory    bool   `json:"keep_history"`
	Wait           bool   `json:"wait"`
	DisableHooks   bool   `json:"disable_hooks"`
	TimeoutSeconds uint32 `json:"timeout_seconds"`
	Description    string `json:"description"`
	DryRun         bool   `json:"dry_run"`
	Confirm        bool   `json:"confirm"`
}

func (handler *helmReleaseWriteHandler) install(c *gin.Context) {
	var request installReleaseRequest
	operation, ok := handler.begin(
		c,
		auditaction.KubernetesHelmReleaseInstall,
		auditaction.KubernetesHelmReleaseInstallDryRun,
		"",
		&request,
		func() (bool, bool, string) {
			return request.DryRun, request.Confirm, request.Name
		},
	)
	if !ok {
		return
	}
	ctx, cancel := handler.operationContext(c)
	report, err := handler.service.Install(ctx, helm.InstallInput{
		ClusterID:          c.Param("cluster_id"),
		Namespace:          c.Param("namespace_name"),
		Name:               request.Name,
		RepositoryID:       request.RepositoryID,
		Chart:              request.Chart,
		Version:            request.Version,
		Values:             request.Values,
		DryRun:             request.DryRun,
		CreateNamespace:    request.CreateNamespace,
		Wait:               request.Wait,
		Atomic:             request.Atomic,
		DisableHooks:       request.DisableHooks,
		TimeoutSeconds:     request.TimeoutSeconds,
		MaxHistory:         request.MaxHistory,
		Description:        handler.description(c, request.Description),
		AllowClusterScoped: operation.allowClusterScoped,
		IdempotencyKey:     c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	handler.finish(c, operation, report, err, "install Helm release", http.StatusCreated)
}

func (handler *helmReleaseWriteHandler) upgrade(c *gin.Context) {
	var request upgradeReleaseRequest
	name := c.Param("release_name")
	operation, ok := handler.begin(
		c,
		auditaction.KubernetesHelmReleaseUpgrade,
		auditaction.KubernetesHelmReleaseUpgradeDryRun,
		name,
		&request,
		func() (bool, bool, string) {
			return request.DryRun, request.Confirm, name
		},
	)
	if !ok {
		return
	}
	ctx, cancel := handler.operationContext(c)
	report, err := handler.service.Upgrade(ctx, helm.UpgradeInput{
		InstallInput: helm.InstallInput{
			ClusterID:          c.Param("cluster_id"),
			Namespace:          c.Param("namespace_name"),
			Name:               name,
			RepositoryID:       request.RepositoryID,
			Chart:              request.Chart,
			Version:            request.Version,
			Values:             request.Values,
			DryRun:             request.DryRun,
			Wait:               request.Wait,
			Atomic:             request.Atomic,
			DisableHooks:       request.DisableHooks,
			TimeoutSeconds:     request.TimeoutSeconds,
			MaxHistory:         request.MaxHistory,
			Description:        handler.description(c, request.Description),
			AllowClusterScoped: operation.allowClusterScoped,
			IdempotencyKey:     c.GetHeader(idempotencyKeyHeaderName),
		},
		ResetValues: request.ResetValues,
		ReuseValues: request.ReuseValues,
	})
	cancel()
	handler.finish(c, operation, report, err, "upgrade Helm release", http.StatusOK)
}

func (handler *helmReleaseWriteHandler) rollback(c *gin.Context) {
	var request rollbackReleaseRequest
	name := c.Param("release_name")
	operation, ok := handler.begin(
		c,
		auditaction.KubernetesHelmReleaseRollback,
		auditaction.KubernetesHelmReleaseRollbackDryRun,
		name,
		&request,
		func() (bool, bool, string) {
			return request.DryRun, request.Confirm, name
		},
	)
	if !ok {
		return
	}
	ctx, cancel := handler.operationContext(c)
	report, err := handler.service.Rollback(ctx, helm.RollbackInput{
		ClusterID:          c.Param("cluster_id"),
		Namespace:          c.Param("namespace_name"),
		Name:               name,
		Revision:           request.Revision,
		DryRun:             request.DryRun,
		Wait:               request.Wait,
		DisableHooks:       request.DisableHooks,
		TimeoutSeconds:     request.TimeoutSeconds,
		MaxHistory:         request.MaxHistory,
		Description:        handler.description(c, request.Description),
		AllowClusterScoped: operation.allowClusterScoped,
		IdempotencyKey:     c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	handler.finish(c, operation, report, err, "roll back Helm release", http.StatusOK)
}

func (handler *helmReleaseWriteHandler) uninstall(c *gin.Context) {
	var request uninstallReleaseRequest
	name := c.Param("release_name")
	operation, ok := handler.begin(
		c,
		auditaction.KubernetesHelmReleaseUninstall,
		auditaction.KubernetesHelmReleaseUninstallDryRun,
		name,
		&request,
		func() (bool, bool, string) {
			return request.DryRun, request.Confirm, name
		},
	)
	if !ok {
		return
	}
	ctx, cancel := handler.operationContext(c)
	report, err := handler.service.Uninstall(ctx, helm.UninstallInput{
		ClusterID:          c.Param("cluster_id"),
		Namespace:          c.Param("namespace_name"),
		Name:               name,
		KeepHistory:        request.KeepHistory,
		DryRun:             request.DryRun,
		Wait:               request.Wait,
		DisableHooks:       request.DisableHooks,
		TimeoutSeconds:     request.TimeoutSeconds,
		Description:        handler.description(c, request.Description),
		AllowClusterScoped: operation.allowClusterScoped,
		IdempotencyKey:     c.GetHeader(idempotencyKeyHeaderName),
	})
	cancel()
	handler.finish(c, operation, report, err, "uninstall Helm release", http.StatusOK)
}

// helmWriteOperation is what begin resolved and finish needs: which action to
// record, against which target, and whether the operator may install objects
// that no Namespace contains.
type helmWriteOperation struct {
	actorUserID        string
	action             string
	target             string
	dryRun             bool
	allowClusterScoped bool
}

// begin performs everything the four handlers share: refuse query parameters,
// decode the body, require confirmation for a real change, and resolve whether
// cluster-scoped objects are allowed. Every early return here is recorded,
// because a refused release change is exactly the kind of attempt an auditor
// asks about afterwards.
func (handler *helmReleaseWriteHandler) begin(
	c *gin.Context,
	action string,
	dryRunAction string,
	name string,
	request any,
	read func() (dryRun bool, confirm bool, name string),
) (helmWriteOperation, bool) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	operation := helmWriteOperation{
		actorUserID: actor.User.ID,
		action:      action,
		target:      helmReleaseTarget(c, name),
	}
	if len(c.Request.URL.Query()) != 0 {
		handler.record(c, operation, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request",
			"Helm release changes do not accept query parameters")
		return operation, false
	}
	if decodeJSONRequest(c, request, maxHelmReleaseRequestBytes) != nil {
		handler.record(c, operation, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Helm release request")
		return operation, false
	}
	dryRun, confirm, resolvedName := read()
	operation.dryRun = dryRun
	operation.target = helmReleaseTarget(c, resolvedName)
	if dryRun {
		operation.action = dryRunAction
	}
	// A dry run writes nothing, so it needs no confirmation; a real one does,
	// exactly as every other Cluster write on this Server does.
	if !dryRun && !confirm {
		handler.record(c, operation, "failed")
		writeError(c, http.StatusBadRequest, "confirmation_required",
			"explicit confirmation is required")
		return operation, false
	}
	if handler.service == nil {
		handler.record(c, operation, "failed")
		writeError(c, http.StatusServiceUnavailable, "unavailable",
			"Helm release management is unavailable")
		return operation, false
	}
	allowed, err := handler.clusterScopedAllowed(c, actor.User.ID)
	if err != nil {
		handler.record(c, operation, "failed")
		handler.respondError(c, "authorize Helm cluster-scoped objects", err)
		return operation, false
	}
	operation.allowClusterScoped = allowed
	return operation, true
}

// clusterScopedAllowed reports whether this operator may install objects that
// are not confined to a Namespace.
//
// It is `cluster.manage` rather than the Helm permission itself: a
// CustomResourceDefinition or a ClusterRoleBinding changes the Cluster, not the
// Namespace the release lives in, so the Namespace-scoped authorization that
// allowed the install says nothing about it. An operator without it can still
// install every chart that stays inside its Namespace; one that does not is
// refused by name, so the refusal says which object caused it.
func (handler *helmReleaseWriteHandler) clusterScopedAllowed(
	c *gin.Context,
	userID string,
) (bool, error) {
	if handler.rbac == nil {
		return false, errors.New("RBAC service is unavailable")
	}
	ctx, cancel := handler.operationContext(c)
	defer cancel()
	_, err := handler.rbac.AuthorizeCluster(
		ctx,
		userID,
		rbac.PermissionClusterManage,
		c.Param("cluster_id"),
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, rbac.ErrDenied) {
		return false, nil
	}
	return false, err
}

// description records who asked for the change on the revision itself, so
// `helm history` outside ZKE shows it too. It is truncated rather than refused:
// a description that is too long is not a reason to refuse a deployment.
func (handler *helmReleaseWriteHandler) description(c *gin.Context, supplied string) string {
	actor, _ := httpmiddleware.Identity(c)
	description := strings.TrimSpace(supplied)
	if actor.User.Username != "" {
		if description == "" {
			description = actor.User.Username
		} else {
			description = actor.User.Username + ": " + description
		}
	}
	if len(description) > helmrelease.MaxDescriptionLength {
		description = description[:helmrelease.MaxDescriptionLength]
	}
	return description
}

func (handler *helmReleaseWriteHandler) finish(
	c *gin.Context,
	operation helmWriteOperation,
	report helmrelease.Report,
	err error,
	description string,
	successStatus int,
) {
	if err != nil {
		handler.record(c, operation, "failed")
		handler.respondHelmWriteError(c, description, err)
		return
	}
	handler.record(c, operation, "succeeded")
	if operation.dryRun {
		successStatus = http.StatusOK
	}
	writeSuccess(c, successStatus, gin.H{
		"release": report,
		"dry_run": operation.dryRun,
	})
}

func (handler *helmReleaseWriteHandler) record(
	c *gin.Context,
	operation helmWriteOperation,
	result string,
) {
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: operation.actorUserID,
		Action:      operation.action,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  operation.target,
		Result:      result,
	})
}

// respondHelmWriteError maps what can go wrong.
//
// A refusal from the Cluster keeps the Cluster's own words: the operator needs
// to read that the chart renders into a Namespace it may not write, or that a
// release of that name already exists, and a generic "the operation failed"
// would send them looking in the wrong place.
func (handler *helmReleaseWriteHandler) respondHelmWriteError(
	c *gin.Context,
	description string,
	err error,
) bool {
	var rejection *helm.ReleaseRejection
	if errors.As(err, &rejection) {
		writeError(
			c,
			helmRejectionStatus(rejection.KubernetesStatusCode),
			helmRejectionCode(rejection.Reason),
			rejection.Message,
		)
		return true
	}
	return handler.respondError(c, description, err,
		errorMapping{
			helm.ErrInvalidInput,
			http.StatusBadRequest,
			"invalid_request",
			"invalid Helm release request",
		},
		errorMapping{
			helm.ErrChartNotFound,
			http.StatusNotFound,
			"chart_not_found",
			"chart or chart version was not found in this repository",
		},
		errorMapping{
			helm.ErrRepositoryNotFound,
			http.StatusNotFound,
			"helm_repository_not_found",
			"Helm chart repository not found",
		},
		errorMapping{
			helm.ErrRepositoryDisabled,
			http.StatusConflict,
			"helm_repository_disabled",
			"Helm chart repository is disabled",
		},
		errorMapping{
			helm.ErrRepositoryUnreachable,
			http.StatusBadGateway,
			"helm_repository_unreachable",
			"Helm chart repository could not be read",
		},
		errorMapping{
			helm.ErrChartTooLarge,
			http.StatusRequestEntityTooLarge,
			"chart_too_large",
			"chart archive exceeds the transferable size",
		},
		errorMapping{
			helm.ErrReportUnreadable,
			http.StatusBadGateway,
			"helm_report_unreadable",
			"the Helm operation finished but its report could not be decoded",
		},
		errorMapping{
			agentconn.ErrHelmCapabilityMissing,
			http.StatusFailedDependency,
			"helm_unsupported",
			"the Agent in this Cluster is too old to manage Helm releases",
		},
		errorMapping{
			agentconn.ErrHelmRequestExhausted,
			http.StatusTooManyRequests,
			"helm_busy",
			"another Helm operation is already running in this Cluster",
		},
		errorMapping{
			agentconn.ErrAgentNotConnected,
			http.StatusServiceUnavailable,
			"agent_unavailable",
			"the Agent in this Cluster is not connected",
		},
	)
}

// helmRejectionStatus keeps the Cluster's own status where it is one an HTTP
// client can act on, and falls back to 422 — the request was understood and the
// Cluster would not do it — rather than to 500, which would blame this Server.
func helmRejectionStatus(code int32) int {
	switch code {
	case http.StatusNotFound,
		http.StatusConflict,
		http.StatusForbidden,
		http.StatusBadRequest,
		http.StatusRequestTimeout,
		http.StatusGatewayTimeout:
		return int(code)
	default:
		return http.StatusUnprocessableEntity
	}
}

// helmRejectionCode turns the Agent's reason into a stable machine code. The
// reason is CamelCase, as Kubernetes writes them; the API's codes are
// lower_snake_case everywhere else and stay that way here.
func helmRejectionCode(reason string) string {
	code := strings.TrimSpace(reason)
	if code == "" {
		return "helm_release_rejected"
	}
	var builder strings.Builder
	builder.Grow(len(code) + 8)
	for index := range len(code) {
		character := code[index]
		switch {
		case character >= 'A' && character <= 'Z':
			if index > 0 {
				builder.WriteByte('_')
			}
			builder.WriteByte(character - 'A' + 'a')
		case (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9'):
			builder.WriteByte(character)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

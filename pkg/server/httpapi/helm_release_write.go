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
	"github.com/togettoyou/zke/pkg/shared/validation"
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
// It does not fit a single HTTP request either, and these four no longer
// pretend that it does. Each starts an operation, answers 202 with its
// identity, and lets the caller read the account of it while it runs — see
// helm.Operations for what that fixes and why the record is held in memory.
// The response body therefore describes an operation, never a release; the
// release is in the report the operation finishes with.
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

// helmOperationCeiling is the backstop deadline on one background operation.
//
// It is not the deadline that normally applies: the Agent Stream bounds a Helm
// request on its own, and Helm bounds its wait by what the operator asked for.
// This covers the whole pipeline including the chart fetch, so a repository
// that accepts a connection and then says nothing cannot leave an operation
// running for the life of the process.
const helmOperationCeiling = 30 * time.Minute

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
	service    helmReleaseWriteService
	rbac       clusterAuthorizer
	operations *helm.Operations
}

func newHelmReleaseWriteHandler(
	logger *slog.Logger,
	service helmReleaseWriteService,
	rbacService clusterAuthorizer,
	auditService *audit.Service,
	operationTimeout time.Duration,
	operations *helm.Operations,
) *helmReleaseWriteHandler {
	return &helmReleaseWriteHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
		rbac:        rbacService,
		operations:  operations,
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
	operation.detail = chartAudit(request.chartReleaseRequest)
	input := helm.InstallInput{
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
		IdempotencyKey:     operation.idempotencyKey,
	}
	handler.start(
		c,
		operation,
		helm.OperationInstall,
		request.Chart,
		request.Version,
		func(ctx context.Context, progress helm.Progress) (helmrelease.Report, error) {
			input.Progress = progress
			return handler.service.Install(ctx, input)
		},
	)
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
	operation.detail = chartAudit(request.chartReleaseRequest)
	input := helm.UpgradeInput{
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
			IdempotencyKey:     operation.idempotencyKey,
		},
		ResetValues: request.ResetValues,
		ReuseValues: request.ReuseValues,
	}
	handler.start(
		c,
		operation,
		helm.OperationUpgrade,
		request.Chart,
		request.Version,
		func(ctx context.Context, progress helm.Progress) (helmrelease.Report, error) {
			input.Progress = progress
			return handler.service.Upgrade(ctx, input)
		},
	)
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
	input := helm.RollbackInput{
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
		IdempotencyKey:     operation.idempotencyKey,
	}
	handler.start(
		c,
		operation,
		helm.OperationRollback,
		"",
		"",
		func(ctx context.Context, progress helm.Progress) (helmrelease.Report, error) {
			input.Progress = progress
			return handler.service.Rollback(ctx, input)
		},
	)
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
	input := helm.UninstallInput{
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
		IdempotencyKey:     operation.idempotencyKey,
	}
	handler.start(
		c,
		operation,
		helm.OperationUninstall,
		"",
		"",
		func(ctx context.Context, progress helm.Progress) (helmrelease.Report, error) {
			input.Progress = progress
			return handler.service.Uninstall(ctx, input)
		},
	)
}

// helmWriteOperation is what begin resolved and start needs: which action to
// record, against which target, and whether the operator may install objects
// that no Namespace contains.
type helmWriteOperation struct {
	actorUserID string
	action      string
	target      string
	releaseName string
	dryRun      bool
	// detail is what the audit row says about the change beyond its target.
	// For an install or an upgrade that is the chart: the target names the
	// release, and "which chart, from which repository, at which version" is
	// the question asked of the trail afterwards — it is the difference between
	// "this release was upgraded" and "this release was moved onto a chart from
	// a repository nobody had approved".
	detail             map[string]string
	allowClusterScoped bool
	idempotencyKey     string
}

// chartAudit records what an install or an upgrade was asked to apply.
//
// The values are what the caller sent, not what the fetch resolved: an empty
// version means "the newest published", and recording it as empty is the honest
// account of a request that did not pin one. The resolved version is in the
// release report the same operation returns.
func chartAudit(request chartReleaseRequest) map[string]string {
	detail := map[string]string{
		"repository_id": request.RepositoryID,
		"chart":         request.Chart,
	}
	if version := strings.TrimSpace(request.Version); version != "" {
		detail["chart_version"] = version
	}
	return detail
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
		actorUserID:    actor.User.ID,
		action:         action,
		target:         helmReleaseTarget(c, name),
		releaseName:    name,
		idempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
	}
	if len(c.Request.URL.Query()) != 0 {
		handler.record(c, operation, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request",
			"Helm release changes do not accept query parameters")
		return operation, false
	}
	// Checked here rather than where the Agent Stream checks it, because by then
	// the operation has a record and a chart has been fetched: a malformed
	// header would surface as an internal failure at the end of a download
	// instead of as a rejected request at the start of one.
	if operation.idempotencyKey != "" &&
		!validation.IsIdempotencyKey(operation.idempotencyKey) {
		handler.record(c, operation, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request",
			"Idempotency-Key is not a valid key")
		return operation, false
	}
	if decodeJSONRequest(c, request, maxHelmReleaseRequestBytes) != nil {
		handler.record(c, operation, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Helm release request")
		return operation, false
	}
	dryRun, confirm, resolvedName := read()
	operation.dryRun = dryRun
	operation.releaseName = resolvedName
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
	if handler.service == nil || handler.operations == nil {
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

// start records the operation, answers with its identity, and runs it.
//
// The reply is 202 and carries no release, because there is not one yet. What
// it carries is the only thing the caller needs in order to find out: the
// identity of an account it can read until the operation finishes and for a
// while afterwards.
//
// A retried request — same idempotency key, same operation — is answered with
// the account that is already running rather than starting a second one, which
// is what makes the retry after a lost 202 harmless.
func (handler *helmReleaseWriteHandler) start(
	c *gin.Context,
	operation helmWriteOperation,
	action helm.OperationAction,
	chart string,
	chartVersion string,
	run func(context.Context, helm.Progress) (helmrelease.Report, error),
) {
	started, existing, err := handler.operations.Start(helm.OperationSpec{
		ClusterID:      c.Param("cluster_id"),
		Namespace:      c.Param("namespace_name"),
		ReleaseName:    operation.releaseName,
		Action:         action,
		DryRun:         operation.dryRun,
		Chart:          chart,
		ChartVersion:   chartVersion,
		ActorUserID:    operation.actorUserID,
		IdempotencyKey: operation.idempotencyKey,
	})
	if err != nil {
		handler.record(c, operation, "failed")
		if errors.Is(err, helm.ErrOperationIdempotencyConflict) {
			writeError(c, http.StatusConflict, "idempotency_conflict", err.Error())
			return
		}
		handler.respondError(c, "start Helm release operation", err)
		return
	}
	if !existing {
		// Captured while the request is still in hand: the gin Context is
		// recycled the moment this handler returns, and the audit row for an
		// operation that outlives it must still say who asked, from where, and
		// under which request.
		record := handler.auditRecorder(c, operation)
		go handler.perform(started.ID, record, run)
	}
	writeSuccess(c, http.StatusAccepted, gin.H{"operation": started})
}

// perform runs one operation to its end and records what it did.
//
// The deadline is the operation's own, not the request's. That is the whole
// point of the change: a Helm install that waits five minutes for a rollout is
// doing exactly what it was asked to do, and cutting it off after the Server's
// ordinary ten-second operation timeout reported a failure that had not
// happened while the Cluster went on and installed the release.
func (handler *helmReleaseWriteHandler) perform(
	identifier string,
	record func(result string),
	run func(context.Context, helm.Progress) (helmrelease.Report, error),
) {
	ctx, cancel := context.WithTimeout(context.Background(), helmOperationCeiling)
	defer cancel()
	report, err := run(ctx, func(stage helm.Stage, message string) {
		handler.operations.Append(identifier, stage, message)
	})
	// Audited before the operation is closed, not after. The account is what a
	// caller watches to learn the change is done, and a change that is done
	// must already be in the trail by the time anybody can be told so.
	if err != nil {
		failure := handler.helmOperationFailure(identifier, err)
		record("failed")
		handler.operations.Finish(identifier, nil, &failure)
		return
	}
	record("succeeded")
	handler.operations.Finish(identifier, &report, nil)
}

// auditRecorder freezes everything an audit row needs while the request that
// authorized it is still available, and hands back the one call that writes it.
func (handler *helmReleaseWriteHandler) auditRecorder(
	c *gin.Context,
	operation helmWriteOperation,
) func(result string) {
	if handler.auditService == nil {
		return func(string) {}
	}
	input := audit.ClusterEventInput{
		ActorUserID: operation.actorUserID,
		ClusterID:   c.Param("cluster_id"),
		Action:      operation.action,
		TargetType:  auditaction.TargetKubernetesResource,
		TargetName:  operation.target,
		RequestID:   httpmiddleware.RequestID(c),
		ActorIP:     c.ClientIP(),
		Detail:      operation.detail,
	}
	// Values only: the request context is cancelled as soon as the handler
	// returns, which for an operation that outlives it is always.
	values := context.WithoutCancel(c.Request.Context())
	logger := handler.logger
	service := handler.auditService
	timeout := handler.operationTimeout
	return func(result string) {
		input.Result = result
		ctx, cancel := context.WithTimeout(values, timeout)
		defer cancel()
		if err := service.RecordClusterEvent(ctx, input); err != nil {
			logger.Error(
				"record Helm release operation audit event",
				slog.String("request_id", input.RequestID),
				slog.String("error", err.Error()),
			)
		}
	}
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
		Detail:      operation.detail,
	})
}

// helmOperationFailure maps what can go wrong onto the code and message the
// caller reads.
//
// A refusal from the Cluster keeps the Cluster's own words: the operator needs
// to read that the chart renders into a Namespace it may not write, or that a
// release of that name already exists, and a generic "the operation failed"
// would send them looking in the wrong place. Anything unrecognised is recorded
// as an internal failure and logged here, never described to the caller.
func (handler *helmReleaseWriteHandler) helmOperationFailure(
	identifier string,
	err error,
) helm.OperationFailure {
	var rejection *helm.ReleaseRejection
	if errors.As(err, &rejection) {
		return helm.OperationFailure{
			Status:  helmRejectionStatus(rejection.KubernetesStatusCode),
			Code:    helmRejectionCode(rejection.Reason),
			Message: rejection.Message,
		}
	}
	for _, mapping := range helmWriteErrorMappings {
		if errors.Is(err, mapping.target) {
			message := mapping.message
			var detailed detailedError
			if errors.As(err, &detailed) && detailed.Detail() != "" {
				message = detailed.Detail()
			}
			return helm.OperationFailure{
				Status:  mapping.status,
				Code:    mapping.code,
				Message: message,
			}
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return helm.OperationFailure{
			Status:  http.StatusGatewayTimeout,
			Code:    "timeout",
			Message: "the Helm operation did not finish within the allowed time",
		}
	case errors.Is(err, context.Canceled):
		return helm.OperationFailure{
			Status:  http.StatusRequestTimeout,
			Code:    "canceled",
			Message: "the Helm operation was canceled",
		}
	}
	handler.logger.Error(
		"run Helm release operation",
		slog.String("helm_operation_id", identifier),
		slog.String("error", err.Error()),
	)
	return helm.OperationFailure{
		Status:  http.StatusInternalServerError,
		Code:    "internal_error",
		Message: "internal server error",
	}
}

// helmWriteErrorMappings is every failure a release change has a name for.
//
// It is a package-level table rather than an argument list because it is now
// consulted from a goroutine with no request to respond to: the operation
// records the same code the synchronous API would have returned, and the
// Console reads it the same way either side of the change.
var helmWriteErrorMappings = []errorMapping{
	{
		helm.ErrInvalidInput,
		http.StatusBadRequest,
		"invalid_request",
		"invalid Helm release request",
	},
	{
		helm.ErrChartNotFound,
		http.StatusNotFound,
		"chart_not_found",
		"chart or chart version was not found in this repository",
	},
	{
		helm.ErrRepositoryNotFound,
		http.StatusNotFound,
		"helm_repository_not_found",
		"Helm chart repository not found",
	},
	{
		helm.ErrRepositoryDisabled,
		http.StatusConflict,
		"helm_repository_disabled",
		"Helm chart repository is disabled",
	},
	{
		helm.ErrRepositoryUnreachable,
		http.StatusBadGateway,
		"helm_repository_unreachable",
		"Helm chart repository could not be read",
	},
	{
		helm.ErrChartTooLarge,
		http.StatusRequestEntityTooLarge,
		"chart_too_large",
		"chart archive exceeds the transferable size",
	},
	// Both refusals happen before a Cluster is contacted, so nothing was
	// written and the idempotency key the caller sent is still theirs to
	// retry with once the cause is fixed.
	{
		helm.ErrChartUnsigned,
		http.StatusUnprocessableEntity,
		"chart_unsigned",
		"this repository requires signed charts and this version publishes no signature",
	},
	{
		helm.ErrChartSignatureInvalid,
		http.StatusUnprocessableEntity,
		"chart_signature_invalid",
		"chart signature did not verify against this repository's keys",
	},
	{
		helm.ErrValuesRejected,
		http.StatusUnprocessableEntity,
		"values_schema_violation",
		"values do not satisfy the chart's own values.schema.json",
	},
	{
		helm.ErrReportUnreadable,
		http.StatusBadGateway,
		"helm_report_unreadable",
		"the Helm operation finished but its report could not be decoded",
	},
	{
		agentconn.ErrHelmCapabilityMissing,
		http.StatusFailedDependency,
		"helm_unsupported",
		"the Agent in this Cluster is too old to manage Helm releases",
	},
	{
		agentconn.ErrHelmRequestExhausted,
		http.StatusTooManyRequests,
		"helm_busy",
		"another Helm operation is already running in this Cluster",
	},
	{
		agentconn.ErrAgentNotConnected,
		http.StatusServiceUnavailable,
		"agent_unavailable",
		"the Agent in this Cluster is not connected",
	},
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
